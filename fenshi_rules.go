package main

// v9f.22 分时规则引擎 v1
// 信号体系（全部可量化，来源：分时图做T策略秘诀/价格行为学/竞价系列资料提炼）：
//  1. 均价线关系：放量突破均价线=买点；持续压在均价线下=弱势
//  2. 量价背离：价新高量缩=顶背离；放量下跌衰竭
//  3. 时间点规律：09:50-10:10 易出高点；10:10-10:40 主力进场；11:10 急拉勿追；14:30 变盘窗口
//  4. 支撑压力：昨收/昨高/昨低
//  5. 大盘共振：上证实时涨跌
// 纯函数设计：输入 trend+ctx，输出信号与倾向评分，便于独立程序验证

import (
	"fmt"
	"time"
)

// FenshiAdvice 综合建议（v9f.22，v9f.24 扩展）
type FenshiAdvice struct {
	Bias       string   `json:"bias"`   // 偏多 / 偏空 / 观望
	Score      float64  `json:"score"`  // -100 ~ +100（加权后净值）
	Reasons    []string `json:"reasons"` // 每条建议的依据
	Suggestion string   `json:"suggestion"` // v9f.24：分层操作建议文案
	Confidence string   `json:"confidence"` // v9f.24：高/中/低（含竞价共振提升）
}

// fenshiRuleIn 规则引擎输入（v9f.21 数据层供给）
type fenshiRuleIn struct {
	Trend     []FenshiTick
	PrevClose float64
	PrevHigh  float64
	PrevLow   float64
	MA5       float64
	MA20      float64
	ShPct     float64
	SzPct     float64 // v9f.23：深成指涨跌%
	CybPct    float64 // v9f.23：创业板指涨跌%
	Now       time.Time
}

// fenshiRunRules 规则引擎主入口
// v9f.24：加权评分——均价线×1.6、量价背离×1.4（趋势核心）；大盘共振×1.2；支撑压力×1.0；时间点×0.6（历史统计，降权）
func fenshiRunRules(in fenshiRuleIn) FenshiAdvice {
	return fenshiRunRulesAdv(in, nil)
}

// fenshiRunRulesAdv 带竞价结论的完整评分（v9f.24）
func fenshiRunRulesAdv(in fenshiRuleIn, auction *FenshiAuctionAdvice) FenshiAdvice {
	adv := FenshiAdvice{Bias: "观望"}
	if len(in.Trend) < 10 {
		adv.Reasons = append(adv.Reasons, "分时数据不足，暂不给倾向")
		return adv
	}
	score := 0.0
	var reasons []string

	// v9f.23.1：评分透明化——同时累计正负分项，末尾展示合计
	posSum, negSum := 0.0, 0.0
	add := func(s float64, why string) {
		score += s
		if s > 0 {
			posSum += s
		} else if s < 0 {
			negSum += -s
		}
		if why != "" {
			reasons = append(reasons, why)
		}
	}
	// v9f.24 加权：核心趋势规则高分权重，统计类低权重
	const wVwap, wDiv, wTime, wSR, wMkt = 1.6, 1.4, 0.6, 1.0, 1.2
	s, why := fenshiRuleVwap(in.Trend)
	add(s*wVwap, why)
	s, why = fenshiRuleDivergence(in.Trend)
	add(s*wDiv, why)
	s, why = fenshiRuleTimePoint(in.Now, in.Trend)
	add(s*wTime, why)
	s, why = fenshiRuleSR(in)
	add(s*wSR, why)
	s, why = fenshiRuleMarket(in)
	add(s*wMkt, why)
	// v9f.25：股笔记提炼四规则（均价线冲击次数/偏离度假强势/回调结构/强控盘）
	s, why = fenshiRuleVwapTouch(in.Trend)
	add(s, why)
	s, why = fenshiRuleDeviation(in.Trend)
	add(s, why)
	s, why = fenshiRulePullback(in.Trend, in.PrevClose)
	add(s, why)
	s, why = fenshiRulePlatform(in.Trend)
	add(s, why)
	// 竞价共振：竞价结论与盘中同向时加权融入（±10 封顶）
	if auction != nil && auction.Score != 0 {
		as := auction.Score / 30 * 10 // 竞价±30 映射到 ±10
		sameDir := (score >= 0 && as > 0) || (score < 0 && as < 0)
		if sameDir {
			add(as, fmt.Sprintf("竞价结论与盘中同向（竞价%s），共振加成", auction.Bias))
		} else {
			add(as*0.3, fmt.Sprintf("竞价结论与盘中相悖（竞价%s），谨慎参考反向项", auction.Bias))
		}
	}

	if score > 100 {
		score = 100
	}
	if score < -100 {
		score = -100
	}
	adv.Score = score
	switch {
	case score >= 30:
		adv.Bias = "偏多"
	case score <= -30:
		adv.Bias = "偏空"
	default:
		adv.Bias = "观望"
	}
	// v9f.24：分层操作建议文案
	adv.Suggestion = fenshiSuggestion(adv.Bias, score, posSum, negSum)
	// v9f.24：置信度——正负项越一边倒越可信；竞价同向提升
	conf := 0.0
	tot := posSum + negSum
	if tot > 0 {
		maj := posSum
		if negSum > maj {
			maj = negSum
		}
		conf = maj / tot // 0.5~1.0，越接近1越一边倒
	}
	switch {
	case conf >= 0.85:
		adv.Confidence = "高"
	case conf >= 0.65:
		adv.Confidence = "中"
	default:
		adv.Confidence = "低"
	}
	if auction != nil && auction.Score != 0 {
		sameDirA := (score >= 0 && auction.Score > 0) || (score < 0 && auction.Score < 0)
		if sameDirA && adv.Confidence == "中" {
			adv.Confidence = "高"
			adv.Reasons = append(adv.Reasons, "竞价与盘中同向，置信度提升为高")
		}
	}
	// v9f.23.1：末尾追加分项合计，让分数可溯源
	adv.Reasons = append(reasons, fmt.Sprintf("评分构成：多方项合计+%g，空方项合计-%g，净值%g（已加权）", posSum, negSum, score))
	return adv
}

// fenshiSuggestion v9f.24：分层操作建议文案
func fenshiSuggestion(bias string, score, posSum, negSum float64) string {
	switch bias {
	case "偏多":
		if score >= 60 {
			return "多头强势：持有者持股待涨；空仓者不追高，等回踩均价线不破再接"
		}
		return "多头占优：持有者可持有；空仓者小仓位试探，破均价线止损"
	case "偏空":
		if score <= -60 {
			return "空头强势：持有者逢反弹减仓；空仓者观望不接刀"
		}
		return "空头占优：持有者留意均价线压力，反弹乏力则减仓；空仓者观望"
	default:
		return "多空拉锯：观望为主，持有者持股观察放量方向，不急于加减仓"
	}
}

// fenshiRuleVwap 均价线关系（近20分钟）
func fenshiRuleVwap(t []FenshiTick) (float64, string) {
	n := len(t)
	if n < 25 {
		// 数据不足25分钟时不评估放量（防 t[n-25:n-5] 越界），只用已有数据判断位置关系
		above := 0
		for _, k := range t {
			if k.Now > k.Avg {
				above++
			}
		}
		ratio := float64(above) / float64(n)
		last := t[n-1]
		switch {
		case ratio >= 0.8 && last.Now > last.Avg:
			return 10, "运行在均价线上方（早盘数据积累中）"
		case ratio <= 0.2 && last.Now < last.Avg:
			return -8, "受压于均价线下方（早盘数据积累中）"
		}
		return 0, ""
	}
	recent := t[n-20:]
	above := 0
	for _, k := range recent {
		if k.Now > k.Avg {
			above++
		}
	}
	ratio := float64(above) / float64(len(recent))
	last := t[n-1]

	switch {
	case ratio >= 0.8 && last.Now > last.Avg && fenshiVolBreak(t):
		return 25, fmt.Sprintf("放量突破均价线（现价%.2f > 均价%.2f），走势偏强", last.Now, last.Avg)
	case ratio >= 0.8 && last.Now > last.Avg:
		return 15, fmt.Sprintf("持续运行在均价线上方（%d/%d分钟），多方占优", above, len(recent))
	case ratio >= 0.8 && last.Now <= last.Avg:
		return -15, "高位回落至均价线附近，放量跌破则转弱"
	case ratio <= 0.2 && last.Now < last.Avg:
		return -10, fmt.Sprintf("持续受压于均价线下方（现价%.2f < 均价%.2f），走势偏弱", last.Now, last.Avg)
	case ratio <= 0.2 && last.Now >= last.Avg:
		return 10, "重新站上均价线，尝试转强"
	}
	return 0, ""
}

// fenshiVolBreak 近5分钟量 > 前20分钟均量1.5倍
func fenshiVolBreak(t []FenshiTick) bool {
	n := len(t)
	if n < 25 {
		return false
	}
	r5 := 0.0
	for _, k := range t[n-5:] {
		r5 += k.Vol
	}
	r5 /= 5 // 每分钟均量，与 p20（20分钟均量）同口径比较
	p20 := 0.0
	for _, k := range t[n-25 : n-5] {
		p20 += k.Vol
	}
	p20 /= 20
	return p20 > 0 && r5 > p20*1.5
}

// fenshiRuleDivergence 量价背离（近30分钟）
func fenshiRuleDivergence(t []FenshiTick) (float64, string) {
	n := len(t)
	if n < 30 {
		return 0, ""
	}
	w := t[n-30:]
	first, last := w[0], w[len(w)-1]
	priceUp := last.Now > first.Now
	priceDn := last.Now < first.Now
	v1, v2 := 0.0, 0.0
	for _, k := range w[:15] {
		v1 += k.Vol
	}
	for _, k := range w[15:] {
		v2 += k.Vol
	}
	volUp := v2 > v1*1.15
	volDn := v2 < v1*0.85

	switch {
	case priceUp && volDn:
		return -12, "30分钟价升量缩（顶背离迹象），追高需谨慎"
	case priceDn && volUp:
		return -8, "放量下跌，空头占优"
	case priceDn && volDn && last.Now < first.Now*0.995:
		return 8, "下跌量能衰竭，或现日内低点"
	case priceUp && volUp:
		return 8, "量价齐升（30分钟），多头健康"
	}
	return 0, ""
}

// fenshiRuleTimePoint 时间点规律
// v9f.23 修正（资料时效性评估：做T秘诀为中期资料，统计性经验降权）：
// 分值 ±5~8 → ±3~5；正向分需量能配合（当前处于放量突破状态才给分）
func fenshiRuleTimePoint(now time.Time, trend []FenshiTick) (float64, string) {
	hm := now.Hour()*100 + now.Minute()
	switch {
	case hm >= 950 && hm <= 1010:
		return -3, "09:50-10:10 历史上易出短期高点，追高需留意量能"
	case hm >= 1010 && hm <= 1040:
		if fenshiVolBreak(trend) {
			return 3, "10:10-10:40 主力进场时段，且当前放量拉升，可信度较高"
		}
		return 0, ""
	case hm >= 1105 && hm <= 1115:
		return -3, "11:10 附近急拉历史上易中招，勿盲目追"
	case hm >= 1430 && hm <= 1450:
		return 0, "14:30 后进入变盘窗口，注意量能方向"
	}
	return 0, ""
}

// fenshiRuleSR 支撑压力位
func fenshiRuleSR(in fenshiRuleIn) (float64, string) {
	if len(in.Trend) == 0 {
		return 0, ""
	}
	last := in.Trend[len(in.Trend)-1]
	if in.PrevHigh > 0 && last.Now < in.PrevHigh && last.Now > in.PrevHigh*0.995 {
		return -8, fmt.Sprintf("接近昨日高点 %.2f 压力位，接近可考虑减仓", in.PrevHigh)
	}
	if in.PrevLow > 0 && last.Now > in.PrevLow && last.Now < in.PrevLow*1.005 {
		return 8, fmt.Sprintf("在昨日低点 %.2f 支撑位附近，跌破需警惕", in.PrevLow)
	}
	return 0, ""
}

// fenshiRuleMarket 大盘共振（v9f.23 修正：三指数同向/分化判定，取代仅看上证）
func fenshiRuleMarket(in fenshiRuleIn) (float64, string) {
	allUp := in.ShPct > 0.3 && in.SzPct > 0.3 && in.CybPct > 0.3
	allDn := in.ShPct < -0.3 && in.SzPct < -0.3 && in.CybPct < -0.3
	switch {
	case allUp:
		return 8, fmt.Sprintf("三大指数同向走强（沪+%.2f%% 深+%.2f%% 创+%.2f%%），环境偏多", in.ShPct, in.SzPct, in.CybPct)
	case allDn:
		return -8, fmt.Sprintf("三大指数同向走弱（沪%.2f%% 深%.2f%% 创%.2f%%），环境偏空", in.ShPct, in.SzPct, in.CybPct)
	}
	// 分化：主板与创业板方向相反，按强弱差给小幅倾向
	if (in.ShPct > 0.3 && in.CybPct < -0.3) || (in.ShPct < -0.3 && in.CybPct > 0.3) {
		s := (in.ShPct - in.CybPct) / 2
		if s > 3 {
			s = 3
		}
		if s < -3 {
			s = -3
		}
		why := ""
		if s > 0 {
			why = fmt.Sprintf("市场分化（沪+%.2f%% / 创%.2f%%），主板偏强", in.ShPct, in.CybPct)
		} else {
			why = fmt.Sprintf("市场分化（沪%.2f%% / 创+%.2f%%），成长偏强", in.ShPct, in.CybPct)
		}
		return s, why
	}
	return 0, ""
}

// ==================== v9f.23 竞价分析（09:15-09:25） ====================
// FenshiAuctionPoint 竞价采样点（Mins: 距 09:15 的分钟数 0-9）
type FenshiAuctionPoint struct {
	Mins  int     `json:"mins"`
	Price float64 `json:"price"`
	Vol   float64 `json:"vol"`
}

// FenshiAuctionAdvice 竞价评分结论
// 来源：竞价短线王系列（2025-07，时效性高）提炼：
//   - 高开 2~5% 且竞价量充足 = 强势承接；高开过多(>5%)谨防诱多
//   - 低开放量 = 抛压大；9:20 后（不可撤单阶段）价格抬升/下压才可信
//   - 竞价量占比 = 竞价成交量 / 昨日全天成交量
type FenshiAuctionAdvice struct {
	Bias     string   `json:"bias"`
	Score    float64  `json:"score"`
	Reasons  []string `json:"reasons"`
	GapPct   float64  `json:"gapPct"`   // 竞价高开%
	VolRatio float64  `json:"volRatio"` // 竞价量占昨日量 ‰
}

// fenshiAuctionEval 竞价评分纯函数（便于独立程序验证）
func fenshiAuctionEval(samples []FenshiAuctionPoint, prevClose, prevVol float64) FenshiAuctionAdvice {
	adv := FenshiAuctionAdvice{Bias: "观望"}
	if prevClose <= 0 || len(samples) == 0 {
		adv.Reasons = append(adv.Reasons, "竞价数据不足")
		return adv
	}
	last := samples[len(samples)-1]
	adv.GapPct = (last.Price - prevClose) / prevClose * 100
	if prevVol > 0 {
		adv.VolRatio = last.Vol / prevVol * 1000 // ‰
	}
	score := 0.0

	// 1. 高开幅度 × 量能
	switch {
	case adv.GapPct >= 2 && adv.GapPct <= 5 && adv.VolRatio >= 3:
		score += 15
		adv.Reasons = append(adv.Reasons, fmt.Sprintf("温和高开 %.2f%% 且竞价量充足（占昨日 %.1f‰），承接强", adv.GapPct, adv.VolRatio))
	case adv.GapPct >= 2 && adv.GapPct <= 5:
		adv.Reasons = append(adv.Reasons, fmt.Sprintf("高开 %.2f%% 但竞价量不足（%.1f‰），防冲高回落", adv.GapPct, adv.VolRatio))
	case adv.GapPct > 5 && adv.VolRatio >= 3:
		score -= 5
		adv.Reasons = append(adv.Reasons, fmt.Sprintf("大幅高开 %.2f%% 放量，谨防诱多出货", adv.GapPct))
	case adv.GapPct > 5:
		score -= 3
		adv.Reasons = append(adv.Reasons, fmt.Sprintf("大幅高开 %.2f%% 无承接，谨慎追", adv.GapPct))
	case adv.GapPct < -3 && adv.VolRatio >= 3:
		score -= 12
		adv.Reasons = append(adv.Reasons, fmt.Sprintf("低开 %.2f%% 且竞价放量（%.1f‰），抛压大", adv.GapPct, adv.VolRatio))
	case adv.GapPct < -3:
		score -= 5
		adv.Reasons = append(adv.Reasons, fmt.Sprintf("低开 %.2f%% 竞价缩量，弱势", adv.GapPct))
	default:
		adv.Reasons = append(adv.Reasons, fmt.Sprintf("开盘 %.2f%%，幅度中性", adv.GapPct))
	}

	// 2. 9:20 后走势（不可撤单阶段，数据可信）
	var at920, after920 []FenshiAuctionPoint
	for _, p := range samples {
		if p.Mins >= 5 {
			after920 = append(after920, p)
		}
	}
	if len(after920) >= 2 && prevClose > 0 {
		at920 = after920[:1]
		first := at920[0].Price
		lastP := after920[len(after920)-1].Price
		if lastP > first*1.005 {
			score += 8
			adv.Reasons = append(adv.Reasons, "9:20 后竞价持续上抬，资金抢筹")
		} else if lastP < first*0.995 {
			score -= 8
			adv.Reasons = append(adv.Reasons, "9:20 后竞价持续下压，抛压出现")
		}
	} else {
		adv.Reasons = append(adv.Reasons, "9:20 后样本不足，走势项未评估")
	}

	if score > 30 {
		score = 30
	}
	if score < -30 {
		score = -30
	}
	adv.Score = score
	switch {
	case score >= 12:
		adv.Bias = "偏多"
	case score <= -12:
		adv.Bias = "偏空"
	}
	return adv
}

// ==================== v9f.25 股笔记（2025-12）提炼规则 ====================

// fenshiRuleVwapTouch 均价线冲击次数（股笔记 P170 + P165 事不过三）
// 现价在均价线下方：累计受压次数，≥4 次未站上 → 弱势（-8）
// 连续 3 次受压回调后，本次带量突破站上均价线 → 事不过三买点（+10）
func fenshiRuleVwapTouch(t []FenshiTick) (float64, string) {
	n := len(t)
	if n < 20 {
		return 0, ""
	}
	// 切分上穿/下穿波段：统计"从下方触及均价线回落"的受压次数
	pressed := 0
	above := false
	belowRun := 0 // 突破前连续在下方运行的分钟数（过滤高频抖动）
	for i := 1; i < n; i++ {
		isAbove := t[i].Now > t[i].Avg
		if isAbove && !above {
			// 向上突破均价线
			if pressed >= 3 && belowRun >= 3 {
				// 事不过三：3 次受压后首次站上
				if fenshiVolBreak(t[:i+1]) {
					return 10, "三次受压后带量突破均价线（事不过三），转强信号"
				}
				return 6, "三次受压后突破均价线，但量能未放量，力度打折"
			}
		} else if !isAbove && above {
			// 从上方跌回均价线下：算一次受压结束
			pressed++
		}
		if isAbove {
			belowRun = 0
		} else {
			belowRun++
		}
		above = isAbove
	}
	// 全程下方运行且多次受压（每次反弹到均线附近回落）
	if pressed >= 4 {
		return -8, fmt.Sprintf("冲击均价线 %d 次未能站上，弱势确认", pressed)
	}
	return 0, ""
}

// fenshiRuleDeviation 偏离度假强势（股笔记 P168/177）
// 股价偏离均价线 >3% 且未封涨停 → 接力断层风险（-8）
// 偏离远且近5分钟量能萎缩 → 拉升不真实（假强势，-10）
func fenshiRuleDeviation(t []FenshiTick) (float64, string) {
	n := len(t)
	if n < 10 {
		return 0, ""
	}
	last := t[n-1]
	if last.Avg <= 0 {
		return 0, ""
	}
	dev := (last.Now - last.Avg) / last.Avg * 100
	if dev > 3 {
		if n >= 25 && !fenshiVolBreak(t) {
			return -10, fmt.Sprintf("偏离均价线 %.1f%% 且量能萎缩，拉升不真实（假强势风险）", dev)
		}
		return -8, fmt.Sprintf("偏离均价线 %.1f%% 且未封板，注意接力断层", dev)
	}
	return 0, ""
}

// fenshiRulePullback 回调结构确认（股笔记 P142 买入三法）
// ① 高开后回落不破昨收，再上穿均价线站稳 → 承接强（+8）
// ② 均价线附近获支撑（回踩不破）→ 上车点（+8）
// ③ 低开 ≤-3% 后急速拉升翻红 → 主力抢筹（+10）
// 取最新满足的一条，避免重复计分
func fenshiRulePullback(t []FenshiTick, prevClose float64) (float64, string) {
	n := len(t)
	if n < 20 || prevClose <= 0 {
		return 0, ""
	}
	// ③ 低开抢筹：首根 K 低开≤-3% 且现价已翻红（高于昨收）
	first := t[0]
	last := t[n-1]
	if first.Now > 0 && first.Now < prevClose && (first.Now-prevClose)/prevClose*100 <= -3 {
		if last.Now > prevClose {
			return 10, "低开后急速拉升翻红，主力逢低抢筹迹象"
		}
	}
	// ① 高开回落不破昨收后上穿均价线
	if n >= 30 {
		mid := t[n/3] // 早盘中段
		if t[0].Now > prevClose && mid.Now < t[0].Now && mid.Now >= prevClose && last.Now > last.Avg {
			return 8, "高开回落不破昨收后重新站稳均价线，承接较强"
		}
	}
	// ② 均价线附近获支撑：近10分钟最低价贴近均价线（±0.3%）但未有效跌破，且现价在均线上方
	minDev := 999.0
	for _, k := range t[n-10:] {
		if k.Avg <= 0 {
			continue
		}
		d := (k.Now - k.Avg) / k.Avg * 100
		if d >= -0.3 && d < minDev {
			minDev = d
		}
	}
	if minDev < 999 && last.Now > last.Avg {
		return 8, "回调至均价线附近获支撑（回踩不破），可关注"
	}
	return 0, ""
}

// fenshiRulePlatform 强控盘平台（股笔记 P164-165）
// 近30分钟振幅 <1.5% 且站稳均价线 → 高控盘；若近5分钟放量突破平台上沿 → +10
func fenshiRulePlatform(t []FenshiTick) (float64, string) {
	n := len(t)
	if n < 35 {
		return 0, ""
	}
	win := t[n-30:]
	hi, lo := win[0].Now, win[0].Now
	for _, k := range win {
		if k.Now > hi {
			hi = k.Now
		}
		if k.Now < lo {
			klo := k.Now
			if klo < lo {
				lo = klo
			}
		}
	}
	if lo <= 0 {
		return 0, ""
	}
	amp := (hi - lo) / lo * 100
	last := t[n-1]
	if amp < 1.5 && last.Now > last.Avg {
		// 平台上沿 = 前25分钟高点
		hi25 := t[n-25].Now
		for _, k := range t[n-25 : n-5] {
			if k.Now > hi25 {
				hi25 = k.Now
			}
		}
		if last.Now > hi25 && fenshiVolBreak(t) {
			return 10, fmt.Sprintf("窄幅横盘（振幅%.1f%%）后放量突破平台上沿，高控盘启动", amp)
		}
		return 4, fmt.Sprintf("窄幅横盘（振幅%.1f%%）站稳均价线，高控盘迹象，等放量突破", amp)
	}
	return 0, ""
}
