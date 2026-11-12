package main

// [local-patch] ===== v8-track 涨幅回填与统计 =====

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"go-stock/backend/db"
	"go-stock/backend/logger"
	"go-stock/backend/models"
)

// tradeDaysAfter 从日线库取某代码在 baseDay 之后的交易日序列（升序）
// day 存储格式 2026-11-06；ComparePrice 为基准价（买入价或买入日收盘）
func tradeDaysAfter(stockCode, baseDay string, n int) ([]models.DailyKLine, bool) {
	var rows []models.DailyKLine
	err := db.Dao.Where("stock_code = ? AND day > ?", stockCode, baseDay).
		Order("day ASC").Limit(n).Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return nil, false
	}
	return rows, true
}

// tradeDaysFrom 从日线库取某代码在 baseDay 当天及之后的交易日序列（升序，含基准日）
func tradeDaysFrom(stockCode, baseDay string, n int) ([]models.DailyKLine, bool) {
	var rows []models.DailyKLine
	err := db.Dao.Where("stock_code = ? AND day >= ?", stockCode, baseDay).
		Order("day ASC").Limit(n).Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return nil, false
	}
	return rows, true
}

// [v9f.17.2] marketTradingDays 全市场交易日历：取大盘股（平安601318）的K线日历作为市场基准
// 返回 day >= baseDay 的前 n 个交易日（含 baseDay 当天，若当天为交易日）
func marketTradingDays(baseDay string, n int) []string {
	var rows []models.DailyKLine
	db.Dao.Where("stock_code = ? AND day >= ?", "601318", baseDay).Order("day ASC").Limit(n).Find(&rows)
	days := make([]string, 0, len(rows))
	for _, r := range rows {
		days = append(days, r.Day)
	}
	return days
}

// basePriceFor 买入基准价：优先手动 BuyPrice；否则买入日收盘价，
// 若买入日非交易日（周末/节假日丢txt）则顺延到下一个交易日收盘作为基准
func basePriceFor(item *models.TrackItem, batch models.TrackBatch) (float64, bool) {
	if item.BuyPrice > 0 {
		return item.BuyPrice, true
	}
	var row models.DailyKLine
	err := db.Dao.Where("stock_code = ? AND day = ?", item.StockCode, batch.TradeDate).First(&row).Error
	if err == nil && row.Close > 0 {
		return row.Close, true
	}
	// 顺延：取 >= 买入日的第一个交易日
	var alt models.DailyKLine
	err = db.Dao.Where("stock_code = ? AND day >= ?", item.StockCode, batch.TradeDate).Order("day ASC").First(&alt).Error
	if err != nil || alt.Close <= 0 {
		return 0, false
	}
	return alt.Close, true
}

// FillBatchHoldings 回填某批次全部票的持仓N日涨幅（增量：按已回填天数跳过）
func FillBatchHoldings(batchID uint) {
	defer PanicHandler()
	var batch models.TrackBatch
	if err := db.Dao.First(&batch, batchID).Error; err != nil {
		return
	}
	var items []models.TrackItem
	db.Dao.Where("batch_id = ?", batchID).Find(&items)
	for i := range items {
		fillItemHoldings(&items[i], batch)
	}
}

// FillAllTrackHoldings [v9f.17.1] 全量幂等回填：每天对所有批次重算，已收盘日期的数据当场补全
// （旧逻辑只回填 filled_days<8 的票，历史批次一旦错过就永远缺数据）
func FillAllTrackHoldings() {
	defer PanicHandler()
	var batchIDs []uint
	db.Dao.Model(&models.TrackBatch{}).Pluck("id", &batchIDs) // 不再限90天：补齐历史后一次补全
	if len(batchIDs) == 0 {
		return
	}
	var items []models.TrackItem
	db.Dao.Where("batch_id IN ?", batchIDs).Find(&items) // 全量重算，fillItemHoldings 内部幂等
	filled := 0
	for i := range items {
		var batch models.TrackBatch
		if err := db.Dao.First(&batch, items[i].BatchID).Error; err != nil {
			continue
		}
		if fillItemHoldings(&items[i], batch) {
			filled++
		}
	}
	if filled > 0 {
		logger.SugaredLogger.Infof("批次涨幅回填完成，更新 %d 只", filled)
		// [v9f.16] 回填后自动同步回测表：数据更新到哪，表就自动追到哪（幂等，无新增则不写盘）
		for _, name := range []string{"一夜持股", "牛指T+1"} {
			var st models.TrackStrategy
			if err := db.Dao.Where("name = ?", name).First(&st).Error; err != nil {
				continue
			}
			SyncStrategySheet(name)
		}
	}
}

// fillItemHoldings 单票增量回填；返回是否有更新
func fillItemHoldings(item *models.TrackItem, batch models.TrackBatch) bool {
	// [v9f] 名称走数据链：回填时顺带用东财现值刷新名称（改名/旧名自动修正，写库+名单表）
	RefreshTrackItemName(item)
	// [v9f.17.1] 不再跳过已填满票：全量重算保证数据完整（重算幂等，值不变不写库）
	// [v9f.17.2] 停牌对齐：按全市场交易日历逐日对齐，个股停牌日标"停"，不挪位不空缺
	// 取 day >= 买入日的K线：第一根为基准日（买入日，或顺延后的下一交易日），其后为持仓日
	rows, ok := tradeDaysFrom(item.StockCode, batch.TradeDate, 9)
	if !ok || len(rows) == 0 {
		// 该股连买入日K线都没有（可能停牌）：看看全市场日历上买入日之后有没有它复牌的数据
		var anyRow models.DailyKLine
		if err := db.Dao.Where("stock_code = ? AND day >= ?", item.StockCode, batch.TradeDate).Order("day ASC").First(&anyRow).Error; err != nil {
			return false // K线完全缺失，等下次同步
		}
		rows = []models.DailyKLine{anyRow}
	}
	base := rows[0].Close
	if item.BuyPrice > 0 {
		base = item.BuyPrice
	}
	// [v9f.17.2] 全市场交易日历（取任意大盘股的K线日历即可，如平安601318；库里有全部市场数据）
	marketDays := marketTradingDays(batch.TradeDate, 9)
	if len(marketDays) == 0 {
		marketDays = nil
	}
	holdRows := rows[1:]
	if len(holdRows) == 0 && len(marketDays) <= 1 {
		return false
	}
	updated := false
	pct := func(px float64) float64 {
		if base <= 0 {
			return 0
		}
		return math.Round((px/base-1)*10000) / 100 // 保留2位小数
	}
	holdSlots := []*float64{&item.Hold1Pct, &item.Hold2Pct, &item.Hold3Pct, &item.Hold4Pct,
		&item.Hold5Pct, &item.Hold6Pct, &item.Hold7Pct, &item.Hold8Pct}
	filled := 0
	// [v9f.17.1] 全量重算：已有值直接覆盖为新计算值（不再增量跳过），确保历史数据一次补全
	// 幂等性：值相同时不置 updated，不产生多余写库
	setVal := func(dst *float64, v float64) {
		if *dst != v {
			*dst = v
			updated = true
		}
	}
	// [v9f.17.2] 按全市场交易日历逐日对齐：个股某交易日无K线=停牌，标进 SuspendDays（显示"停"），不空缺不挪位
	if len(marketDays) > 1 {
		suspends := []string{}
		dayIdx := 0                              // rows 内当前指针（rows[0]是买入日）
		for mIdx, mDay := range marketDays[1:] { // 持仓日从买入日的下一交易日开始
			if mIdx >= len(holdSlots) {
				break
			}
			if dayIdx < len(holdRows) && holdRows[dayIdx].Day == mDay {
				// 正常交易日：填数据
				r := holdRows[dayIdx]
				setVal(holdSlots[mIdx], pct(r.Close))
				if mIdx == 0 {
					if r.Open > 0 {
						setVal(&item.NextOpenPct, pct(r.Open))
					}
					if r.High > 0 {
						setVal(&item.NextHighPct, pct(r.High))
					}
					setVal(&item.NextClosePct, pct(r.Close))
				}
				dayIdx++
			} else {
				// 该股当天无K线 = 停牌（全天无成交）：该持仓日标"停"
				suspends = append(suspends, itoa(mIdx+1))
			}
		}
		sd := strings.Join(suspends, ",")
		if sd != item.SuspendDays {
			item.SuspendDays = sd
			updated = true
		}
		filled = len(marketDays) - 1
	} else {
		// 交易日历取不到：退回旧的按个股序列填法
		for idx, r := range holdRows {
			if idx >= len(holdSlots) {
				break
			}
			setVal(holdSlots[idx], pct(r.Close))
			if idx == 0 {
				if r.Open > 0 {
					setVal(&item.NextOpenPct, pct(r.Open))
				}
				if r.High > 0 {
					setVal(&item.NextHighPct, pct(r.High))
				}
				setVal(&item.NextClosePct, pct(r.Close))
			}
			filled = idx + 1
		}
	}
	// [v9f.17.2] 买入次日停牌：市场有下一个交易日但该股无K线 → 次日细节标"停"
	item.SuspendNext = false
	if len(marketDays) > 1 {
		nextDay := marketDays[1]
		hasK := false
		for _, r := range holdRows {
			if r.Day == nextDay {
				hasK = true
				break
			}
		}
		if !hasK {
			item.SuspendNext = true
			updated = true
		}
	}
	if filled > item.FilledDays {
		item.FilledDays = filled
		updated = true
	} // [v9f.17.1] 全量重算下 filled 只会≥旧值（K线只增不减），保持不变
	// ===== v9b.3: B点式口径回填（原版表格式）=====
	// 当日净涨跌=收/开-1; 收盘涨跌=收/前收-1; 每日最高=第N日高/前日收-1;
	// 区间3=sum(单日1..3); 区间7=sum(单日1..7)-dayChg（口径由原版表4股精确验证）
	fillBpFields(item, batch)
	if updated {
		db.Dao.Save(item)
	}
	return updated
}

// fillBpFields 回填B点式字段（幂等: 已有值跳过）
func fillBpFields(item *models.TrackItem, batch models.TrackBatch) {
	// 买入日 bar（含前收）
	var buyRow models.DailyKLine
	if err := db.Dao.Where("stock_code = ? AND day = ?", item.StockCode, batch.TradeDate).First(&buyRow).Error; err != nil {
		// 顺延第一个交易日
		var alt models.DailyKLine
		if err := db.Dao.Where("stock_code = ? AND day >= ?", item.StockCode, batch.TradeDate).Order("day ASC").First(&alt).Error; err != nil {
			return
		}
		buyRow = alt
	}
	var prevRow models.DailyKLine
	hasPrev := db.Dao.Where("stock_code = ? AND day < ?", item.StockCode, buyRow.Day).Order("day DESC").First(&prevRow).Error == nil
	setIfF := func(dst *float64, v float64) {
		if *dst == 0 && v != 0 {
			*dst = v
		}
	}
	if buyRow.Open > 0 {
		setIfF(&item.DayNetPct, round4(buyRow.Close/buyRow.Open-1))
	}
	if hasPrev && prevRow.Close > 0 {
		setIfF(&item.DayChgPct, round4(buyRow.Close/prevRow.Close-1))
	}
	setIfF(&item.ClosePrice, buyRow.Close)
	// 持仓日: 买入日之后的交易日（与tradeDaysFrom一致）
	rows, ok := tradeDaysAfter(item.StockCode, buyRow.Day, 8)
	if !ok {
		return
	}
	prevClose := buyRow.Close
	single := make([]float64, 0, 8)
	hiSlots := []*float64{&item.High1Pct, &item.High2Pct, &item.High3Pct, &item.High4Pct,
		&item.High5Pct, &item.High6Pct, &item.High7Pct, &item.High8Pct}
	for i, r := range rows {
		if prevClose > 0 {
			setIfF(hiSlots[i], round4(r.High/prevClose-1))
		}
		single = append(single, r.Close/prevClose-1)
		prevClose = r.Close
	}
	// 区间和
	s3 := 0.0
	for i := 0; i < 3 && i < len(single); i++ {
		s3 += single[i]
	}
	setIfF(&item.Sum3Pct, round4(s3))
	if len(single) >= 7 {
		s7 := 0.0
		for i := 0; i < 7; i++ {
			s7 += single[i]
		}
		setIfF(&item.Sum7Pct, round4(s7-item.DayChgPct))
	}
}

func round4(f float64) float64 { return math.Round(f*10000) / 10000 }

// GetTrackStats 分策略统计（基于已回填次日收盘的数据）
func (a *App) GetTrackStats() *[]models.TrackStrategyStat {
	defer PanicHandler()
	var strategies []models.TrackStrategy
	db.Dao.Order("sort_order ASC, id ASC").Find(&strategies)
	out := make([]models.TrackStrategyStat, 0, len(strategies))
	for _, st := range strategies {
		var stat models.TrackStrategyStat
		stat.StrategyID = st.ID
		stat.StrategyName = st.Name
		db.Dao.Model(&models.TrackBatch{}).Where("strategy_id = ?", st.ID).Count(&stat.BatchCount)
		// [v9f.11] 最新批次日期（页签旁显示"批次:MM-DD"）
		db.Dao.Model(&models.TrackBatch{}).Where("strategy_id = ?", st.ID).
			Select("COALESCE(MAX(trade_date),'')").Scan(&stat.LatestBatchDate)
		sql := `SELECT COUNT(*) FROM track_items i JOIN track_batches b ON i.batch_id=b.id WHERE b.strategy_id=?`
		db.Dao.Raw(sql, st.ID).Scan(&stat.ItemCount)
		// 次日收盘统计
		type agg struct {
			Cnt     int64
			Win     int64
			AvgPct  float64
			AvgHigh float64
			WinHigh int64
		}
		var a1 agg
		db.Dao.Raw(`SELECT COUNT(*) as cnt,
				SUM(CASE WHEN i.next_close_pct > 0 THEN 1 ELSE 0 END) as win,
				AVG(i.next_close_pct) as avg_pct,
				AVG(i.next_high_pct) as avg_high,
				SUM(CASE WHEN i.next_high_pct > 0 THEN 1 ELSE 0 END) as win_high
			FROM track_items i JOIN track_batches b ON i.batch_id=b.id
			WHERE b.strategy_id=? AND i.filled_days >= 1`, st.ID).Scan(&a1)
		if a1.Cnt > 0 {
			stat.Hold1WinRate = math.Round(float64(a1.Win)/float64(a1.Cnt)*10000) / 100
			stat.Hold1AvgPct = math.Round(a1.AvgPct*100) / 100
			stat.NextHighAvg = math.Round(a1.AvgHigh*100) / 100
			stat.NextHighWin = math.Round(float64(a1.WinHigh)/float64(a1.Cnt)*10000) / 100
		}
		var a5 agg
		db.Dao.Raw(`SELECT COUNT(*) as cnt,
				SUM(CASE WHEN i.hold5_pct > 0 THEN 1 ELSE 0 END) as win,
				AVG(i.hold5_pct) as avg_pct
			FROM track_items i JOIN track_batches b ON i.batch_id=b.id
			WHERE b.strategy_id=? AND i.hold5_pct <> 0`, st.ID).Scan(&a5)
		if a5.Cnt > 0 {
			stat.Hold5WinRate = math.Round(float64(a5.Win)/float64(a5.Cnt)*10000) / 100
			stat.Hold5AvgPct = math.Round(a5.AvgPct*100) / 100
		}
		out = append(out, stat)
	}
	return &out
}

// ExportTrackCSV 导出某策略全部批次明细为CSV（发灵犀分析用）
func (a *App) ExportTrackCSV(strategyID uint) string {
	defer PanicHandler()
	var st models.TrackStrategy
	if err := db.Dao.First(&st, strategyID).Error; err != nil {
		return "策略不存在"
	}
	var batches []models.TrackBatch
	db.Dao.Where("strategy_id = ?", strategyID).Order("trade_date ASC").Find(&batches)
	if len(batches) == 0 {
		return "该策略暂无批次数据"
	}
	// [v9f.14] CSV防乱：①批次日期/代码加引号，防Excel把日期解析成内部序列值（显示####或乱序）、防代码丢前缀0
	var sb strings.Builder
	sb.WriteString("批次日期,代码,名称,评分,次日开盘%,次日最高%,次日收盘%,持仓3日%,持仓5日%,持仓8日%\n")
	for _, b := range batches {
		var items []models.TrackItem
		db.Dao.Where("batch_id = ?", b.ID).Order("id ASC").Find(&items)
		for _, it := range items {
			fmt.Fprintf(&sb, "\"%s\",\"%s\",%s,%s,%.2f,%.2f,%.2f,%.2f,%.2f,%.2f\n",
				b.TradeDate, it.StockCode, it.StockName, it.Score,
				it.NextOpenPct, it.NextHighPct, it.NextClosePct,
				it.Hold3Pct, it.Hold5Pct, it.Hold8Pct)
		}
	}
	// 写到数据目录
	outPath := trackExportPath(st.Name)
	if err := writeUTF8BOM(outPath, sb.String()); err != nil {
		return "写入失败: " + err.Error()
	}
	openDir(filepath.Dir(outPath))
	return "已导出: " + outPath
}

func trackExportPath(strategyName string) string {
	name := strings.ReplaceAll(strategyName, "/", "_")
	return filepath.Join(filepath.Dir(db.GetDBPath()), fmt.Sprintf("策略跟踪_%s_%s.csv", name, time.Now().Format("20060102_150405")))
}

// writeUTF8BOM 写入带BOM的UTF-8（Excel直接打开中文不乱码）
func writeUTF8BOM(path, content string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		return err
	}
	_, err = f.WriteString(content)
	return err
}

// openDir 用系统文件管理器打开目录
func openDir(path string) {
	switch runtime.GOOS {
	case "windows":
		exec.Command("explorer", path).Start()
	case "darwin":
		exec.Command("open", path).Start()
	default:
		exec.Command("xdg-open", path).Start()
	}
}
