package main

// ==================================================================
// 牛指T+1 每日优选引擎（Go 移植）v9c.4
// 源码对照: 牛指评分脚本_现行版_V4M_限流优化_20261027.py（10657字节 限流优化版）
// 数据源: 本地日线库 daily_kline（与一夜引擎同源，保真对拍口径）
// v9c 改动点(新建):
//   ①灵动12条件(AB1~AB34)逐条保真移植: AB1收昨<收前昨/AB2阳线/AB4上影>下影1.5倍/AB5
//     1.01<高/开<1.09/AB6高<前2高*0.995/AB7低<前2低*0.995/AB8前2收/前1收>1.034/
//     AB9量>=近31根最大量*0.2/AB10收/昨收>1.024/AB12高/收<1.06且高/昨高<1.015/
//     AB32量<=前16根最小量*4/AB34前3日(开,收)极差比<1.04 + 换手>5%
//   ②V4-M评分: 60日回撤<=-33% +2分/-33%~-25% +1分; 缩量(量<=昨量) +1分; 成交额<=2亿 +1分
//   ③强制过滤: ST/退·次新<183天·收盘涨停·高开>2.7%
//   ④三模式仓位: 严选>=3分100%/标准>=2分100%/全勤>=3分100%·2分60%·0-1分30%; 弱市×0.5
//   ⑤排序: 评分降序→成交额升序(Python sort_values(['评分','成交额亿'],asc=[False,True]))
//   ⑥大盘弱市: 上证单日跌>=1%或连跌2日(复用一夜引擎同款逻辑)
// v9c.1 改动点(相对v9c):
//   ⑦前端候选列表按模式过滤: 严选只显示>=3分/标准>=2分/全勤全显(切模式股票数变化直观可见), Go引擎不变(10/10对拍结论不受影响)
// v9c.2 改动点(相对v9c.1):
//   ⑧(已被⑨取代)to=body方案无效
// v9c.3 改动点(相对v9c.2):
//   ⑨修复牛指tab日历点击无效: 放弃独立date-picker实例, 一夜/牛指两tab共用同一个date-picker
//     (一夜上实测正常的那一个物理实例), 仅按tab显隐按钮/模式下拉。Go引擎零改动。
// v9c.4 改动点(相对v9c.3):
//   ⑩两tab日期各记各的: 切tab时自动换回该tab上次用的日期(牛指选股时记忆), 共用组件避免双实例bug
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
	nzMinBars    = 70  // len(d)<70 跳过（脚本同款）
	nzMinIdx     = 61  // i<61 continue
	nzNewDays    = 183 // 次新股天数
	nzHighOpen   = 1.027
	nzZtFilter   = 0.099
	nzAmtMax     = 2e8 // 成交额<=2亿
	nzDD33       = -0.33
	nzDD25       = -0.25
)

// NiuZhiCand 牛指候选结果（对应脚本 CSV 输出列）
type NiuZhiCand struct {
	Code      string  `json:"code"`      // 6位
	Name      string  `json:"name"`
	Price     float64 `json:"price"`     // 现价(收盘)
	Chg       float64 `json:"chg"`       // 今日涨幅%
	Score     int     `json:"score"`     // V4-M评分0~4
	DD60      float64 `json:"dd60"`      // 60日回撤%
	VolShrink bool    `json:"volShrink"` // 缩量
	AmtYi     float64 `json:"amtYi"`     // 成交额亿
	Hsl       float64 `json:"hsl"`       // 换手率%
	PosYan    int     `json:"posYan"`    // 严选仓%
	PosBz     int     `json:"posBz"`     // 标准仓%
	PosQq     int     `json:"posQq"`     // 全勤仓%
	PosFinal  int     `json:"posFinal"`  // 弱市折后最终建议仓%
	TopTag    string  `json:"topTag"`    // Top1/替补
}

// NiuZhiScanResult 结构化结果(前端表格展示)
type NiuZhiScanResult struct {
	Day     string       `json:"day"`
	Trading bool         `json:"trading"`
	Note    string       `json:"note"`
	WeakDay bool         `json:"weakDay"`
	Mode    string       `json:"mode"`    // 严选/标准/全勤
	Cands   []NiuZhiCand `json:"cands"`
}

// nzIsTradingDay 牛指选股交易日判断（与overnightIsTradingDay同款口径）
func nzIsTradingDay(day string) bool {
	d, err := time.Parse("2006-01-02", day)
	if err != nil {
		return false
	}
	today := time.Now().Format("2006-01-02")
	if day >= today {
		if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			return false
		}
		return true
	}
	var cnt int64
	db.Dao.Model(&models.DailyKLine{}).Where("day = ?", day).Limit(1).Count(&cnt)
	return cnt > 0
}

// nzWeakMarket 上证弱市判定: 单日跌>=1% 或 连跌>=2日（脚本口径）
// 返回 (是否弱市, 说明, 数据是否充足)
func nzWeakMarket(day string) (bool, string, bool) {
	var idx []models.DailyKLine
	db.Dao.Where("stock_code = ? AND day <= ?", "000001.SH", day).Order("day DESC").Limit(70).Find(&idx)
	if len(idx) < 6 {
		return false, "上证日线不足, 无法判定弱市(需同步上证指数000001.SH日线)", false
	}
	r1 := idx[0].Close/idx[1].Close - 1
	ld := 0
	for i := 0; i+1 < len(idx); i++ {
		if idx[i].Close < idx[i+1].Close {
			ld++
		} else {
			break
		}
	}
	weak := r1 <= -0.01 || ld >= 2
	note := fmt.Sprintf("大盘今日%.2f%% 连跌%d天 → %s", r1*100, ld, map[bool]string{true: "弱市日(仓位×0.5)", false: "非弱市"}[weak])
	return weak, note, true
}

// RunNiuZhiScanJSON 牛指选股入口(结构化): day空=今天; 历史日期=按当日收盘重演
// mode: 严选/标准/全勤 (空=标准)
func (a *App) RunNiuZhiScanJSON(day, mode string) NiuZhiScanResult {
	defer PanicHandler()
	if day == "" {
		day = time.Now().Format("2006-01-02")
	}
	if mode == "" {
		mode = "标准"
	}
	res := NiuZhiScanResult{Day: day, Mode: mode}
	if !nzIsTradingDay(day) {
		res.Note = day + " 非交易日(周末或节假日), 休市不选股"
		return res
	}
	weak, mktNote, _ := nzWeakMarket(day)
	res.WeakDay = weak
	cands, err := niuzhiScan(day, mode, weak)
	if err != nil {
		res.Note = "选股失败: " + err.Error()
		return res
	}
	res.Trading = true
	res.Cands = cands
	res.Note = mktNote
	logger.SugaredLogger.Infof("牛指T+1 %s[%s]: %d 只候选 | %s", day, mode, len(cands), mktNote)
	return res
}

// RunNiuZhiScan 牛指选股入口(文本结论)
func (a *App) RunNiuZhiScan(day, mode string) string {
	defer PanicHandler()
	r := a.RunNiuZhiScanJSON(day, mode)
	if !r.Trading {
		return "[" + r.Day + "] " + r.Note
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[%s] 牛指T+1候选(%s) %d 只 | %s\n", r.Day, r.Mode, len(r.Cands), r.Note))
	for i, c := range r.Cands {
		if i >= 8 {
			break
		}
		sb.WriteString(fmt.Sprintf("#%d %s %s %.2f %+0.2f%% 评分%d 回撤%+.1f%% 最终仓%d%%%s\n",
			i+1, c.Code, c.Name, c.Price, c.Chg, c.Score, c.DD60, c.PosFinal, c.TopTag))
	}
	return sb.String()
}

// niuzhiScan 核心扫描（数据全走本地日线库，口径与Python脚本逐条对齐）
func niuzhiScan(day, mode string, weak bool) ([]NiuZhiCand, error) {
	// 1) 标的池: 沪60+深00(主板), 非ST, 非次新<183天
	var basics []data.StockBasic
	if err := db.Dao.Where("list_status = ?", "L").Find(&basics).Error; err != nil {
		return nil, err
	}
	type poolItem struct {
		tsCode, name string
	}
	pool := make([]poolItem, 0, 2000)
	dayT, _ := time.Parse("2006-01-02", day)
	for _, b := range basics {
		c := stockCode6(b.TsCode)
		if !strings.HasPrefix(c, "60") && !strings.HasPrefix(c, "00") {
			continue
		}
		if isStName(b.Name) {
			continue
		}
		if b.ListDate != "" {
			if ld, e := time.Parse("20060102", b.ListDate); e == nil {
				if dayT.Before(ld.AddDate(0, 0, nzNewDays)) {
					continue
				}
			}
		}
		pool = append(pool, poolItem{b.TsCode, b.Name})
	}

	// 2) 逐股: 灵动12条件 + V4M评分
	cands := make([]NiuZhiCand, 0, 32)
	for _, b := range pool {
		var bars []models.DailyKLine
		if err := db.Dao.Where("stock_code = ? AND day <= ?", b.tsCode, day).
			Order("day DESC").Limit(nzMinBars + 60).Find(&bars).Error; err != nil || len(bars) < nzMinBars {
			continue
		}
		for i, j := 0, len(bars)-1; i < j; i, j = i+1, j-1 {
			bars[i], bars[j] = bars[j], bars[i]
		}
		if bars[len(bars)-1].Day != day {
			continue // 当日须有日线
		}
		n := len(bars)
		i := n - 1
		if i < nzMinIdx {
			continue
		}
		O := make([]float64, n)
		C := make([]float64, n)
		H := make([]float64, n)
		L := make([]float64, n)
		V := make([]float64, n)
		for k, bar := range bars {
			O[k], C[k], H[k], L[k], V[k] = bar.Open, bar.Close, bar.High, bar.Low, bar.Volume
		}
		o, h, l, c, v := O[i], H[i], L[i], C[i], V[i]

		// ---- 灵动12条件（逐条对应脚本 AB1~AB34）----
		ab1 := C[i-1] < C[i-2]
		ab2 := o < c
		ab4 := (h-c) > (o-l)*1.5
		ab5 := h/o > 1.01 && h/o < 1.09
		ab6 := h < H[i-2]*0.995
		ab7 := l < L[i-2]*0.995
		ab8 := C[i-2]/C[i-1] > 1.034
		// AB9: 量>=近31根(含当日 i-30..i)最大量*0.2
		var v31max float64
		for k := i - 30; k <= i; k++ {
			if V[k] > v31max {
				v31max = V[k]
			}
		}
		ab9 := v >= v31max*0.2
		ab10 := c/C[i-1] > 1.024
		ab12 := h/c < 1.06 && h/H[i-1] < 1.015
		// AB32: 量<=前16根(i-16..i-1)最小量*4
		var v16min float64 = math.MaxFloat64
		for k := i - 16; k < i; k++ {
			if V[k] < v16min {
				v16min = V[k]
			}
		}
		ab32 := v <= v16min*4
		ab34 := math.Max(O[i-3], C[i-3])/math.Min(O[i-3], C[i-3]) < 1.04
		if !(ab1 && ab2 && ab4 && ab5 && ab6 && ab8 && ab9 && ab10 && ab7 && ab12 && ab32 && ab34) {
			continue
		}
		// 换手率>5% (日线库Turnover字段为小数口径0.0796=7.96%, ×100转百分比; 缺失跳过)
		hsl := bars[i].Turnover * 100
		if hsl <= 0 || hsl <= 5 {
			continue
		}
		pctToday := c/C[i-1] - 1
		if pctToday >= nzZtFilter {
			continue // 收盘涨停买不进
		}
		if o >= C[i-1]*nzHighOpen {
			continue // 高开>2.7%剔除
		}

		// ---- V4-M 评分 ----
		lo := i - 59
		if lo < 0 {
			lo = 0
		}
		var c60max float64
		for k := lo; k <= i; k++ {
			if C[k] > c60max {
				c60max = C[k]
			}
		}
		dd60 := c/c60max - 1.0
		volShrink := v <= V[i-1]
		// 成交额: 日线库Amount字段(元), 缺失时用 量*收*100 近似(主板手→股)
		amt := bars[i].Amount
		if amt <= 0 {
			amt = v * c * 100
		}
		score := 0
		if dd60 <= nzDD33 {
			score += 2
		} else if dd60 <= nzDD25 {
			score += 1
		}
		if volShrink {
			score += 1
		}
		if amt <= nzAmtMax {
			score += 1
		}
		// ---- 三模式仓位 ----
		posYan, posBz, posQq := 0, 0, 30
		if score >= 3 {
			posYan, posBz, posQq = 100, 100, 100
		} else if score == 2 {
			posYan, posBz, posQq = 0, 100, 60
		}
		posFinal := posQq
		switch mode {
		case "严选":
			posFinal = posYan
		case "标准":
			posFinal = posBz
		}
		if weak {
			posFinal = posFinal / 2 // 弱市×0.5
		}
		cands = append(cands, NiuZhiCand{
			Code: stockCode6(b.tsCode), Name: b.name,
			Price: round2(c), Chg: round2(pctToday*100), Score: score,
			DD60: round1(dd60*100), VolShrink: volShrink, AmtYi: round2(amt/1e8),
			Hsl: round2(hsl), PosYan: posYan, PosBz: posBz, PosQq: posQq, PosFinal: posFinal,
		})
	}

	if len(cands) == 0 {
		return nil, nil
	}
	// 排序: 评分降序→成交额升序（Python sort_values(['评分','成交额亿'],ascending=[False,True])）
	sort.Slice(cands, func(x, y int) bool {
		if cands[x].Score != cands[y].Score {
			return cands[x].Score > cands[y].Score
		}
		return cands[x].AmtYi < cands[y].AmtYi
	})
	for i := range cands {
		if i == 0 {
			cands[i].TopTag = "Top1"
		} else if i == 1 {
			cands[i].TopTag = "替补"
		}
	}
	return cands, nil
}
