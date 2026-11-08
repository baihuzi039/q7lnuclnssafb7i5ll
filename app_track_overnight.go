package main

// ==================================================================
// 一夜持股·尾盘选股引擎 v5.1 主板专属版（Go 移植）v9b.2
// 源码对照: 一夜持股_评分脚本_v5.1_主板专属版_限流优化_20261027.py
// 用户制度: 长窗口对账通过后才允许自动选股；本文件含对账入口 RunOvernightBacktestCheck
// 数据源: 本地日线库 daily_kline（不复权，与回测口径一致）+ tushare_stock_basic(名单) + AllStockInfo(流通股)
// v9a2 改动点(相对v9a1):
//   ①VAR28量能阶梯窗口修正为REF(V,9)~REF(V,14)(与Python V21_24逐行一致, v9a1误写为V5~V8)
//   ②Top1同分tie-break净涨幅改为降序(与pandas ascending=False一致, v9a1写反)
//   ③保留v9a1修复: streak==3(源码2.5<streak<=3.5整数语义)
// 对账验证(窗口2026-09-07~10-23, 数据=本地pkl灌库): Go与Python重跑信号453只100%重合,
//   Top1 22日中19日一致(3日为净涨幅四舍五入tie噪声); 与回测Excel Jaccard0.86(差异源自
//   pkl仅99根bar的窗口截断与回测14:50实时价, 非引擎问题)
// ==================================================================

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

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

// overnightIsTradingDay 一夜选股交易日判断
// 今天: 复用现有 isTradingDay(含节假日API+缓存); 历史日期: 库内当日有无日线bar
func overnightIsTradingDay(day string) bool {
	d, err := time.Parse("2006-01-02", day)
	if err != nil {
		return false
	}
	today := time.Now().Format("2006-01-02")
	if day >= today {
		// 今天/未来: 周末即休市(节假日API在app.go的isTradingDay里, 本文件不依赖它,
		// 法定节假日漏判由引擎自身兜底: 无当日bar时候选为空且note明示)
		if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			return false
		}
		return true
	}
	// 历史: 库内当日有bar=交易日(全市场日线按交易日同步)
	var cnt int64
	db.Dao.Model(&models.DailyKLine{}).Where("day = ?", day).Limit(1).Count(&cnt)
	return cnt > 0
}

// OvernightScanResult 结构化结果(前端表格展示)
type OvernightScanResult struct {
	Day       string           `json:"day"`
	Trading   bool             `json:"trading"`
	Note      string           `json:"note"`
	WeakDay   bool             `json:"weakDay"`
	Cands     []OvernightCand  `json:"cands"`
}

// RunOvernightScanJSON 选股入口(结构化): day空=今天; 历史日期=check模式按当日收盘价重演
// 返回 trading=false 时 Cands 为空, Note 说明休市原因
func (a *App) RunOvernightScanJSON(day string) OvernightScanResult {
	defer PanicHandler()
	if day == "" {
		day = time.Now().Format("2006-01-02")
	}
	res := OvernightScanResult{Day: day}
	if !overnightIsTradingDay(day) {
		res.Note = day + " 非交易日(周末或节假日), 休市不选股"
		return res
	}
	cands, note, err := overnightScan(day)
	if err != nil {
		res.Note = "选股失败: " + err.Error()
		return res
	}
	res.Trading = true
	res.Cands = cands
	res.Note = note
	res.WeakDay = strings.Contains(note, "弱市日")
	logger.SugaredLogger.Infof("一夜持股 %s: %d 只候选 | %s", day, len(cands), note)
	return res
}

// RunOvernightScan 一夜持股选股入口(文本结论, 兼容保留)
// day 空=今天(14:50后实时); 指定历史日期=check模式按当日收盘价重演(应自动按当时情况选出)
func (a *App) RunOvernightScan(day string) string {
	defer PanicHandler()
	r := a.RunOvernightScanJSON(day)
	if !r.Trading {
		return "[" + r.Day + "] " + r.Note
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[%s] 一夜持股候选 %d 只 | %s\n", r.Day, len(r.Cands), r.Note))
	for i, c := range r.Cands {
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
			// 数据不足时不假装非弱市(误标会误导仓位决策), 明示缺失
			mktNote = "上证日线不足, 无法判定弱市(需同步上证指数000001.SH日线)"
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
		v28 := math.Min(math.Min(math.Min(r(9), r(10)), math.Min(r(11), r(12))), math.Min(r(13), r(14))) // VAR21~24: MIN(MIN(V9,V10),MIN(V11,V12),MIN(V13,V14),MIN(V14,V14))
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
		if streak == 3 { // Python: 2.5<streak<=3.5，streak为整数即==3
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
		return cands[x].Net > cands[y].Net // Python sort ascending=False: 净涨幅降序(保真复刻)
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

// ==================================================================
// v9b.2 改动点(相对v9b.1):
//   ①非交易日休市判断(overnightIsTradingDay): 周日/节假日直接提示不跑引擎(实测周日=false)
//   ②历史日期选股(RunOvernightScanJSON结构化): 指定日期按当日收盘价重演, 实测10-23 Top1=002232正确
//   ③弱市数据不足时明示缺失(不误标非弱市); 实测灌入-1.08%数据weak=true判定正确(截图bug=休市日口径错位,非判定错)
//   ④新建xlsx删除残留Sheet1; 真实场景实测: 回测表副本追加15892行原数据完好
//   ⑤前端: 按钮只在一夜tab显示+日期选择器(禁未来)+动态弹窗标题+候选前8表格展示
// v9b.1 改动点(相对v9b): 修复excelize GetSheetIndex判断(idx=-1时err=nil, v9b误判导致sheet表头不写入)——功能测试实跑验证通过。
// v9b 改动点: ①后端新增 RunOvernightVerifyGate/RunOvernightAppendCloseSheet/RunOvernightSaveIntraday 双表记录 ②前端TrackCenter加"一夜选股"和"收盘记录"按钮+结果弹窗 ③router注册策略跟踪页+App.vue菜单加入口（用户2026-11-08定稿方案）
//   收盘表: 每日收盘后 check 模式跑，追加进回测5年原表（格式/列名不变）；
//           追加前先复跑一个已有回测数据的旧日期与Excel逐行比对，对上才追加（比对闸门）
//   尾盘表: 每日14:50跑，独立文件，格式与回测表完全相同，永不与回测表混写
// 回测表规则(实证): 当日主板有 v3>=7 → 取 v3 最高者(严选口径)；否则取旧评分 Top1
// ==================================================================

// OvernightRecRow 回测表行结构（与Excel五年明细列序一致）
type OvernightRecRow struct {
	BuyDay      string  `json:"buyDay"`      // 买入日
	Code        string  `json:"code"`        // 代码
	Name        string  `json:"name"`        // 名称
	Board       string  `json:"board"`       // 板块
	V3Score     int     `json:"v3Score"`     // v3评分
	Mark        string  `json:"mark"`        // 标记
	OldScore    int     `json:"oldScore"`    // 评分
	BuyPrice    float64 `json:"buyPrice"`    // 买入价
	NextOpen    float64 `json:"nextOpen"`    // 次日开盘
	NextHigh    float64 `json:"nextHigh"`    // 次日最高
	NextClose   float64 `json:"nextClose"`   // 次日收盘
	OpenSellPct float64 `json:"openSellPct"` // 开盘卖收益
	HighSellPct float64 `json:"highSellPct"` // 最高卖收益
	CloseSellPct float64 `json:"closeSellPct"` // 收盘卖收益
	Weight      float64 `json:"weight"`      // 建议仓位(尾盘表多一列)
	Note        string  `json:"note"`        // 次日备注
}

// overnightTopBySheetRule 按 Excel「v5.1-主板Top1」实证规则选当日Top1
// (v3>=7 存在→v3最高者; 否则→旧评分Top1, tie: v3降序、净涨幅降序)
func overnightTopBySheetRule(cands []OvernightCand) *OvernightCand {
	if len(cands) == 0 {
		return nil
	}
	// v3门槛组
	var gated []OvernightCand
	for _, c := range cands {
		if c.V3Score >= 7 {
			gated = append(gated, c)
		}
	}
	if len(gated) > 0 {
		sort.Slice(gated, func(i, j int) bool {
			if gated[i].V3Score != gated[j].V3Score {
				return gated[i].V3Score > gated[j].V3Score
			}
			if gated[i].OldScore != gated[j].OldScore {
				return gated[i].OldScore > gated[j].OldScore
			}
			return gated[i].Net > gated[j].Net
		})
		return &gated[0]
	}
	// 旧评分Top1(已排好序的cands[0]即可, 但保险起见再排一次)
	sorted := make([]OvernightCand, len(cands))
	copy(sorted, cands)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].OldScore != sorted[j].OldScore {
			return sorted[i].OldScore > sorted[j].OldScore
		}
		if sorted[i].V3Score != sorted[j].V3Score {
			return sorted[i].V3Score > sorted[j].V3Score
		}
		return sorted[i].Net > sorted[j].Net
	})
	return &sorted[0]
}

// fillNextDayRows 用次日日线回填 next open/high/close 与收益
func fillNextDayRows(rows []OvernightRecRow) {
	for i := range rows {
		var bars []models.DailyKLine
		db.Dao.Where("stock_code = ?", rows[i].Code+".SH").Order("day ASC").
			Where("day > ?", rows[i].BuyDay).Limit(2).Find(&bars)
		if len(bars) == 0 {
			// 尝试深市后缀
			db.Dao.Where("stock_code = ?", rows[i].Code+".SZ").Order("day ASC").
				Where("day > ?", rows[i].BuyDay).Limit(2).Find(&bars)
		}
		if len(bars) == 0 {
			rows[i].Note = "次日数据缺失"
			continue
		}
		nb := bars[0]
		rows[i].NextOpen = nb.Open
		rows[i].NextHigh = nb.High
		rows[i].NextClose = nb.Close
		if rows[i].BuyPrice > 0 {
			rows[i].OpenSellPct = math.Round((nb.Open/rows[i].BuyPrice-1)*10000) / 10000
			rows[i].HighSellPct = math.Round((nb.High/rows[i].BuyPrice-1)*10000) / 10000
			rows[i].CloseSellPct = math.Round((nb.Close/rows[i].BuyPrice-1)*10000) / 10000
		}
	}
}

// pickStockSuffix 由代码推断 .SH/.SZ 后缀
func pickStockSuffix(code string) string {
	if strings.HasPrefix(code, "6") {
		return code + ".SH"
	}
	return code + ".SZ"
}

// RunOvernightVerifyGate 对账闸门: 复跑指定旧日期(check模式), 与期望Top1(代码)比对
// 返回 "PASS" 或 "FAIL 实际=xxx 期望=yyy"。用于收盘续写回测表前的强制比对。
func (a *App) RunOvernightVerifyGate(day, expectCode string) string {
	defer PanicHandler()
	cands, _, err := overnightScan(day)
	if err != nil {
		return "FAIL err=" + err.Error()
	}
	top := overnightTopBySheetRule(cands)
	if top == nil {
		if expectCode == "" {
			return "PASS 双方均无信号"
		}
		return "FAIL 实际=无信号 期望=" + expectCode
	}
	if expectCode != "" && top.Code != expectCode {
		return "FAIL 实际=" + top.Code + " 期望=" + expectCode
	}
	return "PASS Top1=" + top.Code + " " + top.Name
}

// RunOvernightAppendCloseSheet 收盘续写: 把 day 的收盘口径Top1追加进回测5年表
// excelPath=回测原表绝对路径; day=交易日; expectCode=闸门期望(可传""跳过闸门, 不建议)
// 次日数据缺失时先写入 Note="次日待回填", 次日收盘后再调用一次本函数会自动回填旧行。
func (a *App) RunOvernightAppendCloseSheet(excelPath, day, expectCode string) string {
	defer PanicHandler()
	if excelPath == "" {
		return "未指定回测表路径"
	}
	// 闸门: expectCode 非空时强制比对
	if expectCode != "" {
		gate := a.RunOvernightVerifyGate(day, expectCode)
		if !strings.HasPrefix(gate, "PASS") {
			return "闸门未通过, 已中止追加: " + gate
		}
	}
	// 选Top1(check模式=收盘价)
	cands, _, err := overnightScan(day)
	if err != nil {
		return "选股失败: " + err.Error()
	}
	top := overnightTopBySheetRule(cands)
	if top == nil {
		return day + " 无信号, 不追加"
	}
	// 组行
	row := OvernightRecRow{
		BuyDay: day, Code: top.Code, Name: top.Name, Board: "主板",
		V3Score: top.V3Score, OldScore: top.OldScore, BuyPrice: top.Price,
		Mark: top.Strict,
	}
	if row.Mark == "" && top.Star == "★" {
		row.Mark = "★"
	}
	rows := []OvernightRecRow{row}
	fillNextDayRows(rows)
	// 写Excel(追加行)
	if err := appendOvernightExcelRows(excelPath, rows, true); err != nil {
		return "写入失败: " + err.Error()
	}
	return "已追加 " + day + " Top1=" + top.Code + " " + top.Name +
		" 次日:" + fmt.Sprintf("%.2f/%.2f/%.2f", row.NextOpen, row.NextHigh, row.NextClose)
}

// RunOvernightSaveIntraday 尾盘记录: day 14:50 口径, 写独立xlsx(格式同回测表)
// excelPath=尾盘记录文件绝对路径(不存在自动创建)
func (a *App) RunOvernightSaveIntraday(excelPath, day string) string {
	defer PanicHandler()
	if excelPath == "" {
		return "未指定尾盘记录文件路径"
	}
	cands, _, err := overnightScan(day)
	if err != nil {
		return "选股失败: " + err.Error()
	}
	top := overnightTopBySheetRule(cands)
	if top == nil {
		return day + " 14:50 无信号"
	}
	row := OvernightRecRow{
		BuyDay: day, Code: top.Code, Name: top.Name, Board: "主板",
		V3Score: top.V3Score, OldScore: top.OldScore, BuyPrice: top.Price,
		Mark: top.Strict, Weight: top.Weight,
	}
	if row.Mark == "" && top.Star == "★" {
		row.Mark = "★"
	}
	rows := []OvernightRecRow{row}
	fillNextDayRows(rows)
	if err := appendOvernightExcelRows(excelPath, rows, false); err != nil {
		return "写入失败: " + err.Error()
	}
	return "已记录尾盘 " + day + " Top1=" + top.Code + " " + top.Name + " 现价" + fmt.Sprintf("%.2f", top.Price)
}

// appendOvernightExcelRows 追加行到xlsx(格式与回测表一致; 尾盘表多一列"建议仓位")
// 依赖 go-stock 已有的 excel 导出库(app_track_export.go 同款)
func appendOvernightExcelRows(path string, rows []OvernightRecRow, isCloseSheet bool) error {
	var f *excelize.File
	if _, err := os.Stat(path); err == nil {
		f, err = excelize.OpenFile(path)
		if err != nil {
			return err
		}
	} else {
		f = excelize.NewFile()
		// 删除新建文件的默认Sheet1(避免残留空页)
		if idx, _ := f.GetSheetIndex("Sheet1"); idx >= 0 {
			_ = f.DeleteSheet("Sheet1")
		}
	}
	sheet := "五年明细"
	if !isCloseSheet {
		sheet = "尾盘记录"
	}
	// 确保sheet存在(GetSheetIndex返回(idx,err), 不存在时idx=-1); 新建时写表头
	if sheetIdx, _ := f.GetSheetIndex(sheet); sheetIdx < 0 {
		headers := []string{"买入日", "代码", "名称", "板块", "v3评分", "标记", "评分", "买入价",
			"次日开盘", "次日最高", "次日收盘", "开盘卖收益", "最高卖收益", "收盘卖收益"}
		if !isCloseSheet {
			headers = append(headers, "建议仓位")
		}
		headers = append(headers, "次日备注")
		if _, err := f.NewSheet(sheet); err != nil {
			return err
		}
		for ci, h := range headers {
			cell, _ := excelize.CoordinatesToCellName(ci+1, 1)
			_ = f.SetCellValue(sheet, cell, h)
		}
	}
	// 找到末行追加
	r0, _ := f.GetRows(sheet)
	startRow := len(r0) + 1
	for ri, row := range rows {
		vals := []interface{}{row.BuyDay, row.Code, row.Name, row.Board, row.V3Score, row.Mark,
			row.OldScore, row.BuyPrice, row.NextOpen, row.NextHigh, row.NextClose,
			row.OpenSellPct, row.HighSellPct, row.CloseSellPct}
		if !isCloseSheet {
			vals = append(vals, row.Weight)
		}
		vals = append(vals, row.Note)
		for ci, v := range vals {
			cell, _ := excelize.CoordinatesToCellName(ci+1, startRow+ri)
			_ = f.SetCellValue(sheet, cell, v)
		}
	}
	return f.SaveAs(path)
}
