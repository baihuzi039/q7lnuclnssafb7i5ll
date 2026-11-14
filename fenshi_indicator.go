package main

import (
	"fmt"
	"strconv"
)

// v9f.28：通达信"分时源码A"指标翻译落地
// 数据边界：现手买卖(BUYVOL/SELLVOL)为L2逐笔数据，本软件无该数据源，对应段落跳过；
// 机构资金A2/A3原意为单笔成交额>20万的方向统计，此处用分钟级近似（分钟额>160万且分钟收涨/跌）。

// FenshiIndMark 买卖信号标记
type FenshiIndMark struct {
	Time  string  `json:"time"`  // 分钟 "0930"
	Kind  string  `json:"kind"`  // buy / sell
	Price float64 `json:"price"` // 标记价
}

// FenshiIndA 分时源码A输出
type FenshiIndA struct {
	Ok      bool    `json:"ok"`
	Resist  float64 `json:"resist"`  // 阻力 L1+P1*7/8
	Support float64 `json:"support"` // 支撑 L1+P1*0.5/8
	Mid     float64 `json:"mid"`     // 中轴
	Score   int     `json:"score"`

	TrendTxt string `json:"trendTxt"` // 多头强势/多头震荡/空头弱势/空头震荡
	VolTxt   string `json:"volTxt"`   // 放量/平量/缩量
	MoneyTxt string `json:"moneyTxt"` // 资金流向 ±N%
	MoneyAtt string `json:"moneyAtt"` // 积极做多/偏多/偏空/积极做空
	PosTxt   string `json:"posTxt"`   // 突破阻力/强势区/弱势区/跌破支撑
	AvgRel   string `json:"avgRel"`   // ↑均价/↓均价/≈均价

	Advice string `json:"advice"` // ★强推(70) 积极做多 等
	Detail string `json:"detail"` // 建议详情

	Marks []FenshiIndMark `json:"marks"` // 买卖信号点
}

// fenshiIndEMA 通达信EMA(X,N)：Y=(X*2+Y'*(N-1))/(N+1)，递推
func fenshiIndEMA(vals []float64, n int) []float64 {
	out := make([]float64, len(vals))
	if len(vals) == 0 {
		return out
	}
	out[0] = vals[0]
	k := 2.0 / float64(n+1)
	for i := 1; i < len(vals); i++ {
		out[i] = vals[i]*k + out[i-1]*(1-k)
	}
	return out
}

// fenshiApplyIndicatorA 在分析结果上计算分时源码A并写入 res.IndA
func fenshiApplyIndicatorA(res *FenshiAnalysisResult) {
	if res == nil || len(res.Trend) < 5 || res.PrevClose <= 0 {
		return
	}
	ind := FenshiIndA{Ok: true, Marks: []FenshiIndMark{}}

	// ---- 阻力/支撑/中轴（DYNAINFO(3)昨收、(5)最高、(6)最低，当日高低从分钟线取）----
	dayHigh, dayLow := res.Trend[0].Now, res.Trend[0].Now
	for _, t := range res.Trend {
		if t.High > dayHigh {
			dayHigh = t.High
		}
		if t.Low < dayLow {
			dayLow = t.Low
		}
	}
	H1 := res.PrevClose
	if dayHigh > H1 {
		H1 = dayHigh
	}
	L1 := res.PrevClose
	if dayLow < L1 {
		L1 = dayLow
	}
	P1 := H1 - L1
	if P1 <= 0 {
		P1 = res.PrevClose * 0.005 // 极端一字板兜底
	}
	ind.Resist = L1 + P1*7/8
	ind.Support = L1 + P1*0.5/8
	ind.Mid = (ind.Support + ind.Resist) / 2

	// ---- 序列 ----
	n := len(res.Trend)
	closes := make([]float64, n)
	for i, t := range res.Trend {
		closes[i] = t.Now
	}
	ma30 := fenshiIndEMA(closes, 30)
	strong := fenshiIndEMA(closes, 900)

	// ---- 机构资金近似（A2/A3）----
	// 原码：成交额/8>20（万元）即单笔额>160万；分钟近似：分钟额>160万且分钟收涨/跌
	var A2, A3 float64
	for i := 1; i < n; i++ {
		amt := res.Trend[i].Vol * res.Trend[i].Now // 分钟成交额(元)
		if amt/8 > 20*10000 {
			if closes[i] > closes[i-1] {
				A2 += amt
			} else if closes[i] < closes[i-1] {
				A3 += amt
			}
		}
	}
	A6 := A2 + A3
	flow := A2 - A3
	if A6 > 0 {
		// v9f.28.2：取绝对值四舍五入再定符号，负数不能直接 int(pct+0.5)（向零截断会吞掉 -1）
		pct := flow / A6 * 100
		a := -pct
		if a < 0 {
			a = -a
		}
		r := int(a + 0.5)
		if pct > 0.5 {
			ind.MoneyTxt = "+" + strconv.Itoa(r) + "%"
		} else if pct < -0.5 {
			ind.MoneyTxt = "-" + strconv.Itoa(r) + "%"
		} else {
			ind.MoneyTxt = "0%"
		}
	} else {
		ind.MoneyTxt = "0%"
	}
	if A2 > A3*1.5 {
		ind.MoneyAtt = "积极做多"
	} else if A2 > A3 {
		ind.MoneyAtt = "偏多"
	} else if A3 > A2*1.5 {
		ind.MoneyAtt = "积极做空"
	} else {
		ind.MoneyAtt = "偏空"
	}

	// ---- 量比（快照值，已在 res.VolumeRate；无则按分钟量推）----
	vr := res.VolumeRate
	if vr <= 0 {
		vr = 1
	}
	if vr > 1.5 {
		ind.VolTxt = "放量"
	} else if vr > 0.8 {
		ind.VolTxt = "平量"
	} else {
		ind.VolTxt = "缩量"
	}

	// ---- 位置 ----
	now := closes[n-1]
	switch {
	case now > ind.Resist:
		ind.PosTxt = "突破阻力"
	case now > ind.Mid:
		ind.PosTxt = "强势区"
	case now > ind.Support:
		ind.PosTxt = "弱势区"
	default:
		ind.PosTxt = "跌破支撑"
	}

	// ---- 均价关系 ----
	if now > res.AvgPrice {
		ind.AvgRel = "↑均价"
	} else if now < res.AvgPrice {
		ind.AvgRel = "↓均价"
	} else {
		ind.AvgRel = "≈均价"
	}

	// ---- 趋势文字 ----
	trendDir := ma30[n-1] - strong[n-1]
	ma30Prev := ma30[n-4]
	if ma30Prev < 0 {
		ma30Prev = ma30[0]
	}
	switch {
	case trendDir > 0 && ma30[n-1] > ma30Prev:
		ind.TrendTxt = "多头强势"
	case trendDir > 0:
		ind.TrendTxt = "多头震荡"
	case trendDir < 0 && ma30[n-1] < ma30Prev:
		ind.TrendTxt = "空头弱势"
	default:
		ind.TrendTxt = "空头震荡"
	}

	// ---- 综合评分 ----
	score := 0
	if ma30[n-1] > strong[n-1] {
		score += 30
	}
	if now > res.AvgPrice {
		score += 20
	}
	if A2 > A3 {
		score += 30
	}
	if now > ind.Support {
		score += 20
	}
	ind.Score = score

	switch {
	case score >= 70:
		ind.Advice = fmt.Sprintf("★强推(%d) 积极做多", score)
		ind.Detail = "趋势量能资金偏多,支撑低吸阻力高抛"
	case score >= 50:
		ind.Advice = fmt.Sprintf("☆关注(%d) 逢低吸纳", score)
		ind.Detail = "趋势向上但存分歧,回踩支撑轻仓试多"
	case score >= 30:
		ind.Advice = fmt.Sprintf("△观望(%d) 等待信号", score)
		ind.Detail = "多空不明交投清淡,等待明朗再操作"
	case score >= 15:
		ind.Advice = fmt.Sprintf("○减仓(%d) 控制风险", score)
		ind.Detail = "空头信号增多,反弹减仓等底部企稳"
	default:
		ind.Advice = fmt.Sprintf("X规避(%d) 离场观望", score)
		ind.Detail = "空头趋势资金流出,不建议参与等反转"
	}

	// ---- 买卖信号（按通达信语义）----
	// CROSS(支撑,现价)：现价跌破支撑（支撑线上穿现价）
	// LONGCROSS(支撑,现价,2)：现价在支撑下方连续≥2分钟后上穿支撑 → 回升买点
	// CROSS(现价,阻力)：现价上破阻力
	belowRun := 0 // 现价连续在支撑下方的分钟数
	for i := 1; i < n; i++ {
		p0, p1 := closes[i-1], closes[i]
		// LONGCROSS(支撑,现价,2)：下方持续≥2分钟后上穿支撑 → 买
		if p1 >= ind.Support && p0 < ind.Support && belowRun >= 2 {
			ind.Marks = append(ind.Marks, FenshiIndMark{Time: res.Trend[i].Time, Kind: "buy", Price: p1 * 0.9975})
		}
		// CROSS(支撑,现价)：现价跌破支撑 → STICKLINE 柱体信号（无文字）
		if p1 < ind.Support && p0 >= ind.Support {
			belowRun = 1
		} else if p1 < ind.Support {
			belowRun++
		} else {
			belowRun = 0
		}
		// CROSS(现价,阻力)：现价上破阻力 → 卖
		if p1 > ind.Resist && p0 <= ind.Resist {
			ind.Marks = append(ind.Marks, FenshiIndMark{Time: res.Trend[i].Time, Kind: "sell", Price: p1 * 1.0025})
		}
	}

	res.IndA = &ind
}


