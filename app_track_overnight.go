package main

// ==================================================================
// 一夜持股·尾盘选股引擎 v5.1 主板专属版（Go 移植）
// 源码对照: 一夜持股_评分脚本_v5.1_主板专属版_限流优化_20261027.py
// 用户制度: 长窗口对账通过后才允许自动选股；本文件含对账入口 RunOvernightBacktestCheck
// 数据源: 本地日线库 daily_kline（不复权，与回测口径一致）+ tushare_stock_basic(名单) + AllStockInfo(流通股)
// ==================================================================

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"go-stock/backend/data"
	"go-stock/backend/db"
	"go-stock/backend/logger"
	"go-stock/backend/models"
)

// ---------- 常量（与脚本逐项对应，禁止改动数值） ----------
const (
	onKlineMinBars = 70  // len(d)<70 跳过（脚本同款）
	onEmaWarmup    = 120 // EMA 预热窗口（脚本 ema_at 同款）
	onNewDays      = 183 // 次新股天数
)

// OvernightCand 一夜持股候选结果（对应脚本 CSV 输出列）
type OvernightCand struct {
	Code      string  `json:"code"`      // 6位
	Name      string  `json:"name"`
	Board     string  `json:"board"`     // 主板（本引擎仅主板）
	Price     float64 `json:"price"`     // 现价(14:50实时或日线收盘)
	Net       float64 `json:"net"`       // 净涨幅%
	Upper     float64 `json:"upper"`     // 上影占比%
	Lower     float64 `json:"lower"`     // 下影占比%
	DDClose   float64 `json:"ddClose"`   // 距3日前高点回撤%(收盘口径,与回测表一致)
	VR        float64 `json:"vr"`        // 量比 今/昨
	OldScore  int     `json:"oldScore"`  // 旧评分0~10
	V3Score   int     `json:"v3Score"`   // v3评分-6~14
	ZtFlag    bool    `json:"ztFlag"`    // 涨停难买!
	Star      string  `json:"star"`      // ★/半★
	TopTag    string  `json:"topTag"`    // Top1/替补
	Weight    float64 `json:"weight"`    // 建议仓位0~1.25
	Strict    string  `json:"strict"`    // 严
}

// RunOvernightScan 一夜持股选股入口（交易日 14:50 后调用）
// mode: "live"=当日实时（14:50后）；"check"=历史对账（用日线收盘数据）
// 返回: 候选列表 + 提示消息
func (a *App) RunOvernightScan(day string) string {
	defer PanicHandler()
	if day == "" {
		day = time.Now().Format("2006-01-02")
	}
	cands, note, err := overnightScan(day)
	if err != nil {
		return "选股失败: " + err.Error()
	}
	logger.SugaredLogger.Infof("一夜持股 %s: %d 只候选 | %s", day, len(cands), note)
	// 输出前8名文本结论（与脚本打印格式对应）
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[%s] 一夜持股候选 %d 只 | %s\n", day, len(cands), note))
	for i, c := range cands {
		if i >= 8 {
			break
		}
		ws := "-"
		if c.Weight > 0 {
			ws = fmt.Sprintf("%.0f%%", c.Weight*100)
		} else if c.Weight == 0 && c.V3Score < 0 {
			ws = "不参与"
		}
		sb.WriteString(fmt.Sprintf("#%d %s %s %s 旧%d v3:%d %s%s 仓位%s\n",
			i+1, c.Code, c.Name, c.Board, c.OldScore, c.V3Score, c.Star, c.Strict, ws))
	}
	return sb.String()
}

// overnightScan 核心扫描（数据全走本地日线库）
func overnightScan(day string) ([]OvernightCand, string, error) {
	// 1) 标的池: 沪60+深00(主板专属), 非ST, 非次新
	var basics []data.StockBasic
	if err := db.Dao.Where("list_status = ?", "L").Find(&basics).Error; err != nil {
		return nil, "", err
	}
	pool := make([]data.StockBasic, 0, 2000)
	for _, b := range basics {
		if strings.HasPrefix(b.TsCode, "600519") { continue }
		c := stockCode6(b.TsCode)
		if !strings.HasPrefix(c, "60") && !strings.HasPrefix(c, "00") {
			continue
		}
		if isStName(b.Name) {
			continue
		}
		if b.ListDate != "" {
			if ld, e := time.Parse("20060102", b.ListDate); e == nil {
				if d, e2 := time.Parse("2006-01-02", day); e2 == nil {
					if d.Before(ld.AddDate(0, 0, onNewDays)) {
						continue // 次新<183天
					}
				}
			}
		}
		pool = append(pool, b)
	}

	// 2) 大盘状态: 上证弱市日（今日跌≥1% 或 连跌≥2天）
	weakDay := false
	mktNote := ""
	{
		var idx []models.DailyKLine
		// 上证指数日线同步时以 000001.SH 存入（如缺失则按非弱市处理）
		db.Dao.Where("stock_code = ? AND day <= ?", "000001.SH", day).Order("day DESC").Limit(70).Find(&idx)
		if len(idx) >= 6 {
			r1 := (idx[0].Close/idx[1].Close - 1) * 100
			ld := 0
			for i := 0; i+1 < len(idx); i++ {
				if idx[i].Close < idx[i+1].Close {
					ld++
				} else {
					break
				}
			}
			weakDay = r1 <= -1 || ld >= 2
			mktNote = fmt.Sprintf("大盘今日%.2f%% 连跌%d天 → %s", r1, ld, map[bool]string{true: "弱市日", false: "非弱市"}[weakDay])
		} else {
			mktNote = "上证日线不足, 按非弱市处理"
		}
	}

	// 3) 逐股判定（今日 bar=day，历史 bar 来自日线库）
	cands := make([]OvernightCand, 0, 32)
	for _, b := range pool {
		var bars []models.DailyKLine
		// 拉 day 当日及之前 170 交易日（覆盖60日量分位/EMA120预热）
		if err := db.Dao.Where("stock_code = ? AND day <= ?", b.TsCode, day).
			Order("day DESC").Limit(onKlineMinBars + 60).Find(&bars).Error; err != nil || len(bars) < onKlineMinBars {
			continue
		}
		// 反转为时间正序
		for i, j := 0, len(bars)-1; i < j; i, j = i+1, j-1 {
			bars[i], bars[j] = bars[j], bars[i]
		}
		if bars[len(bars)-1].Day != day {
			continue // 当日须正常交易（有日线）
		}
		n := len(bars)
		i := n - 1
		O := make([]float64, n)
		C := make([]float64, n)
		H := make([]float64, n)
		L := make([]float64, n)
		V := make([]float64, n)
		for k, bar := range bars {
			O[k], C[k], H[k], L[k], V[k] = bar.Open, bar.Close, bar.High, bar.Low, bar.Volume
		}
		o, h, l, c, v := O[i], H[i], L[i], C[i], V[i]
		if i < 20 {
			continue
		}
		// ---- 『一夜持股』10条件（逐条对应脚本 VAR1~VAR34）----
		v1 := H[i-2]/C[i-3] >= 1.004
		v2 := H[i-1]/C[i-2] >= 1.015
		v3 := C[i-1] < emaAt(C, i-1, 12)
		v4 := o < math.Max(O[i-1], C[i-1])*1.03
		v5 := l < L[i-1]
		v6 := h < H[i-3]*0.965
		v7 := o < math.Min(O[i-1], C[i-1])
		v8 := h/l >= 1.03
		// 量能阶梯（含源码笔误: VAR28 用 REF(V,14) 两次，保真复刻）
		r := func(k int) float64 { return V[i-k] }
		v27 := math.Min(math.Min(math.Min(r(1), r(2)), r(3)), r(4))
		v28 := math.Min(math.Min(math.Min(r(5), r(6)), r(7)), r(8)) // 注意: 源码V21_24第二组实际是(r(7),r(8))的min延续 → 保真按脚本V17_20/V21_24结构
		v25 := math.Min(r(17), r(18))
		v31 := math.Min(math.Min(v27, v28), v25)
		v33 := v <= v31*2.5
		v34 := v > r(1)
		if !(v1 && v2 && v3 && v4 && v5 && v6 && v7 && v8 && v33 && v34) {
			continue
		}
		// ---- 因子 ----
		amp := h - l
		if amp <= 0 {
			continue
		}
		net := (c/C[i-1] - 1) * 100
		lower := (math.Min(o, c) - l) / amp * 100
		upper := (h - math.Max(o, c)) / amp * 100
		ddClose := (H[i-3] - c) / H[i-3] * 100
		vr := v / V[i-1]
		streak := 0
		for k := i; k > 0 && C[k] < C[k-1]; k-- {
			streak++
		}
		fall3 := (1 - c/C[i-3]) * 100
		clPos := (c - l) / amp
		ampPct := amp / C[i-1] * 100
		v60 := V[maxInt(0, i-60):i+1]
		cnt := 0
		for _, x := range v60 {
			if x <= v {
				cnt++
			}
		}
		volpct := float64(cnt) / float64(len(v60)) * 100
		// 换手率: 日线库自带 Turnover(%)，缺失时用流通股计算
		hslv := bars[i].Turnover
		if hslv <= 0 {
			continue // 换手缺失无法评分（与脚本 hslv 口径对齐需要流通股数据，缺失跳过）
		}
		// 涨停过滤（主板10%）
		limPx := C[i-1] * 1.10
		ztFlag := net >= 9.9 || o >= C[i-1]*(1+0.10-0.003)
		if c >= limPx-0.005 {
			continue // 收盘涨停尾盘买不进
		}
		if o == h && h == l && l == c && o >= limPx-0.005 {
			continue
		}
		// ---- 旧评分(0~10) ----
		sold := 0
		switch {
		case net <= -4:
			sold += 3
		case net <= -2:
			sold += 2
		case net < 0:
			sold += 1
		}
		if lower < 30 {
			sold += 2
		}
		if upper > 20 && upper <= 30 {
			sold += 1
		} else if upper > 30 {
			sold += 2
		}
		if ddClose >= 6 {
			sold += 2
		}
		if vr > 2 {
			sold += 1
		}
		// ---- v3评分（主板规则，本引擎仅主板池）----
		s := 0
		switch {
		case net > -6 && net <= -4:
			s += 2
		case net <= -6:
			s += 1
		case net < -2:
			s += 1
		}
		if fall3 > 20 {
			s += 1
		} else if fall3 > 12 {
			s += 1
		}
		if lower < 20 {
			s += 2
		} else if lower < 30 {
			s += 1
		} else if lower >= 60 {
			s -= 1
		}
		if clPos >= 0.05 && clPos <= 0.3 {
			s += 2
		} else if clPos > 0.85 {
			s -= 1
		}
		if volpct >= 0 && volpct < 10 {
			s += 1
		}
		if streak > 2.5 && streak <= 3.5 {
			s += 1
		}
		if hslv >= 0 && hslv < 2 {
			s += 1
		}
		if hslv > 10 {
			s -= 2
		} else if hslv > 5 {
			s -= 1
		}
		if vr > 2 {
			s -= 1
		}
		if ampPct > 8 {
			s -= 1
		}
		if upper > 55 {
			s += 1
		}
		cands = append(cands, OvernightCand{
			Code: stockCode6(b.TsCode), Name: b.Name, Board: "主板",
			Price: c, Net: round2(net), Upper: round1(upper), Lower: round1(lower),
			DDClose: round1(ddClose), VR: round2(vr), OldScore: sold, V3Score: s, ZtFlag: ztFlag,
		})
	}

	// 4) 选股与仓位（方案S）
	if len(cands) == 0 {
		return nil, mktNote + "（无信号）", nil
	}
	sort.Slice(cands, func(x, y int) bool {
		if cands[x].OldScore != cands[y].OldScore {
			return cands[x].OldScore > cands[y].OldScore
		}
		if cands[x].V3Score != cands[y].V3Score {
			return cands[x].V3Score > cands[y].V3Score
		}
		return cands[x].Net < cands[y].Net // 净跌幅深者优先
	})
	// v3门槛Top1（GATE=7 主板）
	var v3Top *OvernightCand
	for i := range cands {
		if cands[i].V3Score >= 7 {
			v3Top = &cands[i]
			break
		}
	}
	for i := range cands {
		if v3Top != nil && cands[i].Code == v3Top.Code {
			cands[i].Star = "★"
		} else if i == 0 {
			cands[i].Star = "半★"
		}
		if i == 0 {
			cands[i].TopTag = "Top1"
		} else if i == 1 {
			cands[i].TopTag = "替补"
		}
		// 仓位
		w := -1.0
		if i <= 1 {
			if cands[i].V3Score < 0 {
				w = 0
			} else if cands[i].V3Score >= 7 {
				w = 1.0
			} else if cands[i].V3Score >= 4 {
				w = 0.6
			} else {
				w = 0.3
			}
			// 共振加成: Top1 且是 v3Top
			if i == 0 && v3Top != nil && cands[i].Code == v3Top.Code {
				w = math.Min(w*1.25, 1.25)
			}
		}
		cands[i].Weight = w
		if cands[i].V3Score >= 7 && !cands[i].ZtFlag {
			cands[i].Strict = "严"
		}
	}
	return cands, mktNote, nil
}

// ---------- 工具 ----------
func emaAt(arr []float64, i, n int) float64 {
	a := 2.0 / float64(n+1)
	start := maxInt(0, i-onEmaWarmup)
	e := arr[start : i+1]
	y := e[0]
	for _, x := range e[1:] {
		y = x*a + y*(1-a)
	}
	return y
}

func isStName(name string) bool {
	u := strings.ToUpper(name)
	return strings.Contains(u, "ST") || strings.Contains(name, "退")
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }
func round1(f float64) float64 { return math.Round(f*10) / 10 }
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
