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

// FenshiAdvice 综合建议（v9f.22）
type FenshiAdvice struct {
	Bias    string   `json:"bias"`   // 偏多 / 偏空 / 观望
	Score   float64  `json:"score"`  // -100 ~ +100（正多负空）
	Reasons []string `json:"reasons"` // 每条建议的依据
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
	Now       time.Time
}

// fenshiRunRules 规则引擎主入口
func fenshiRunRules(in fenshiRuleIn) FenshiAdvice {
	adv := FenshiAdvice{Bias: "观望"}
	if len(in.Trend) < 10 {
		adv.Reasons = append(adv.Reasons, "分时数据不足，暂不给倾向")
		return adv
	}
	score := 0.0
	var reasons []string

	s, why := fenshiRuleVwap(in.Trend)
	score += s
	if why != "" {
		reasons = append(reasons, why)
	}
	s, why = fenshiRuleDivergence(in.Trend)
	score += s
	if why != "" {
		reasons = append(reasons, why)
	}
	s, why = fenshiRuleTimePoint(in.Now)
	score += s
	if why != "" {
		reasons = append(reasons, why)
	}
	s, why = fenshiRuleSR(in)
	score += s
	if why != "" {
		reasons = append(reasons, why)
	}
	s, why = fenshiRuleMarket(in)
	score += s
	if why != "" {
		reasons = append(reasons, why)
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
	adv.Reasons = reasons
	return adv
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

// fenshiRuleTimePoint 时间点规律（做T秘诀六时间点）
func fenshiRuleTimePoint(now time.Time) (float64, string) {
	hm := now.Hour()*100 + now.Minute()
	switch {
	case hm >= 950 && hm <= 1010:
		return -5, "09:50-10:10 易出短期高点（高抛窗口），追高易挂"
	case hm >= 1010 && hm <= 1040:
		return 5, "10:10-10:40 为主力进场时段，放量拉升可信度较高"
	case hm >= 1105 && hm <= 1115:
		return -8, "11:10 附近急拉勿追（易中招），除非市场极强"
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

// fenshiRuleMarket 大盘共振
func fenshiRuleMarket(in fenshiRuleIn) (float64, string) {
	switch {
	case in.ShPct > 0.5:
		return 6, fmt.Sprintf("大盘走强（上证+%.2f%%），环境偏多", in.ShPct)
	case in.ShPct < -0.5:
		return -6, fmt.Sprintf("大盘走弱（上证%.2f%%），环境偏空", in.ShPct)
	}
	return 0, ""
}
