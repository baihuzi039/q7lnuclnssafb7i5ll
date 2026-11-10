package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
	"go-stock/backend/db"
	"go-stock/backend/logger"
	"go-stock/backend/models"
)

// ============ 原版Excel模板导出（v9-export） ============
// 用户定案：原版备份不动，导出时 复制模板→按原格式追加新数据→另存为新文件
// 模板位置：data/templates/<策略名>.xlsx（用户手动放入原版文件副本）
// 输出位置：data/export/<策略名>_<批次日期>.xlsx

// ExportTrackExcel 按原版模板导出指定批次，返回结果消息（成功=文件路径）
func (a *App) ExportTrackExcel(strategyID, batchID uint) string {
	defer PanicHandler()
	var st models.TrackStrategy
	if err := db.Dao.First(&st, strategyID).Error; err != nil {
		return "策略不存在"
	}
	var batch models.TrackBatch
	if err := db.Dao.First(&batch, batchID).Error; err != nil {
		return "批次不存在"
	}
	var items []models.TrackItem
	db.Dao.Where("batch_id = ?", batchID).Order("id ASC").Find(&items)
	if len(items) == 0 {
		return "批次无明细数据"
	}

	tplDir := filepath.Join(filepath.Dir(db.GetDBPath()), "templates")
	tplPath := filepath.Join(tplDir, st.Name+".xlsx")
	if _, err := os.Stat(tplPath); err != nil {
		return "缺少模板文件: " + tplPath + "（请把原版Excel改名为「" + st.Name + ".xlsx」放入该目录）"
	}

	outDir := filepath.Join(filepath.Dir(db.GetDBPath()), "export")
	os.MkdirAll(outDir, 0755)
	outPath := filepath.Join(outDir, fmt.Sprintf("%s_%s_%d.xlsx", st.Name, batch.TradeDate, batch.ID))

	var err error
	switch st.Name {
	case "一夜持股":
		err = exportYiye(tplPath, outPath, items, batch)
	default:
		return "该策略的模板导出即将支持（当前先上：一夜持股）"
	}
	if err != nil {
		return "导出失败: " + err.Error()
	}
	openDir(outDir)
	return outPath
}

// exportYiye 一夜持股：追加到「五年明细」sheet
// 原版列(15): A买入日@ B代码@ C名称@ D板块@ E v3评分0 F标记@ G评分0 H买入价0.00
//
//	I次日开盘0.00 J次日最高0.00 K次日收盘0.00 L开盘卖收益% M最高卖收益% N收盘卖收益% O次日备注@
func exportYiye(tplPath, outPath string, items []models.TrackItem, batch models.TrackBatch) error {
	data, err := os.ReadFile(tplPath)
	if err != nil {
		return err
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("模板打开失败: %w", err)
	}
	defer f.Close()
	const sheet = "五年明细"
	rows, err := f.GetRows(sheet)
	if err != nil {
		return err
	}
	// 追加起始行 = 最后一个非空A列行 + 1
	start := len(rows) + 1
	for r := len(rows); r >= 1; r-- {
		if len(rows[r-1]) > 0 && rows[r-1][0] != "" {
			start = r + 1
			break
		}
	}
	srcRow := start - 1 // 样式来源行（最后一条数据行）

	for _, it := range items {
		row := itoa(start)
		f.SetCellStr(sheet, "A"+row, batch.TradeDate)
		f.SetCellStr(sheet, "B"+row, stockCode6(it.StockCode))
		f.SetCellStr(sheet, "C"+row, it.StockName)
		f.SetCellStr(sheet, "D"+row, boardName(it.StockCode))
		// E v3评分（txt未提供则留空）；G 评分
		if it.Score != "" {
			f.SetCellStr(sheet, "G"+row, it.Score)
		}
		// H 买入价（手动价优先，否则买入日/顺延日收盘）
		base := it.BuyPrice
		if base <= 0 {
			if b, ok := trackBasePrice(it, batch); ok {
				base = b
			}
		}
		if base > 0 {
			f.SetCellFloat(sheet, "H"+row, base, 2, 64)
		}
		// I/J/K 次日开盘/最高/收盘（回填数据）；L/M/N 收益% 用公式保持模板惯例
		if it.NextOpenPct != 0 && base > 0 {
			f.SetCellFloat(sheet, "I"+row, base*(1+it.NextOpenPct/100), 2, 64)
		}
		if it.NextHighPct != 0 && base > 0 {
			f.SetCellFloat(sheet, "J"+row, base*(1+it.NextHighPct/100), 2, 64)
		}
		if it.NextClosePct != 0 && base > 0 {
			f.SetCellFloat(sheet, "K"+row, base*(1+it.NextClosePct/100), 2, 64)
		}
		f.SetCellStr(sheet, "O"+row, nextNote(batch.TradeDate))
		start++
	}
	// 复制最后一条数据行的样式到全部新行（保持模板外观）
	copyRowStyles(f, sheet, srcRow, srcRow+1, start-1, 15)
	return f.SaveAs(outPath)
}

// stockCode6 取6位纯数字代码（600519.SH → 600519）
func stockCode6(code string) string {
	if i := strings.Index(code, "."); i > 0 {
		return code[:i]
	}
	return code
}

// boardName 由代码推断板块
func boardName(code string) string {
	c := stockCode6(code)
	if c == "" {
		return ""
	}
	switch {
	case strings.HasPrefix(c, "60"), strings.HasPrefix(c, "00"):
		return "主板"
	case strings.HasPrefix(c, "30"):
		return "创业板"
	case strings.HasPrefix(c, "68"):
		return "科创板"
	case strings.HasPrefix(c, "8"), strings.HasPrefix(c, "4"):
		return "北交所"
	}
	return ""
}

// trackBasePrice 查买入基准价（当日无K线则顺延下一交易日）
func trackBasePrice(item models.TrackItem, batch models.TrackBatch) (float64, bool) {
	var row models.DailyKLine
	err := db.Dao.Where("stock_code = ? AND day = ?", item.StockCode, batch.TradeDate).First(&row).Error
	if err == nil && row.Close > 0 {
		return row.Close, true
	}
	err = db.Dao.Where("stock_code = ? AND day >= ?", item.StockCode, batch.TradeDate).Order("day ASC").First(&row).Error
	if err != nil || row.Close <= 0 {
		return 0, false
	}
	return row.Close, true
}

// copyRowStyles 复制样式行到目标行区间（逐列复制单元格样式ID）
func copyRowStyles(f *excelize.File, sheet string, srcRow, dstStart, dstEnd, cols int) {
	styleIDs := make([]int, 0, cols)
	for c := 1; c <= cols; c++ {
		sid, _ := f.GetCellStyle(sheet, colLetter(c)+itoa(srcRow))
		styleIDs = append(styleIDs, sid)
	}
	for r := dstStart; r <= dstEnd; r++ {
		for c := 1; c <= cols; c++ {
			if styleIDs[c-1] > 0 {
				f.SetCellStyle(sheet, colLetter(c)+itoa(r), colLetter(c)+itoa(r), styleIDs[c-1])
			}
		}
	}
}

// nextNote 次日备注（模板惯例：导入日写"待MM-DD"=下一交易日，17:40回填后可改）
func nextNote(tradeDate string) string {
	t, err := time.Parse("2006-01-02", tradeDate)
	if err != nil {
		return "待次日"
	}
	return "待" + t.AddDate(0, 0, 1).Format("01-02")
}

func itoa(i int) string { return fmt.Sprintf("%d", i) }

// colLetter 列号转字母（1→A, 15→O）
func colLetter(n int) string {
	s := ""
	for n > 0 {
		n--
		s = string(rune('A'+n%26)) + s
		n /= 26
	}
	return s
}

// BackfillTrackRange v9e.9.7 一键补齐历史区间信号日（用户需求：回测表信号日只到某日，
// 之后到今天的空档由引擎按历史日线重演补建批次，持仓涨幅自动回填，得到最新完整回测表）
func (a *App) BackfillTrackRange(strategyName, startDate, endDate string) string {
	defer PanicHandler()
	parse := func(s string) (time.Time, error) {
		s = strings.TrimSpace(s)
		if d, err := time.Parse("2006-01-02", s); err == nil {
			return d, nil
		}
		return time.Parse("20060102", s)
	}
	sd, err := parse(startDate)
	if err != nil {
		return "开始日期格式错误（示例 2026-11-01）"
	}
	ed, err := parse(endDate)
	if err != nil {
		return "结束日期格式错误（示例 2026-11-10）"
	}
	if ed.After(time.Now()) {
		ed = time.Now()
	}
	var st models.TrackStrategy
	if err := db.Dao.Where("name = ?", strategyName).First(&st).Error; err != nil {
		return "找不到策略: " + strategyName
	}
	aa := &App{}
	created, empty, skip := 0, 0, 0
	notes := []string{}
	for d := sd; !d.After(ed); d = d.AddDate(0, 0, 1) {
		if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			continue
		}
		day := d.Format("2006-01-02")
		var cnt int64
		db.Dao.Model(&models.TrackBatch{}).Where("strategy_id = ? AND trade_date = ?", st.ID, day).Count(&cnt)
		if cnt > 0 {
			skip++
			continue
		}
		var lines []string
		switch strategyName {
		case "一夜持股":
			r := aa.RunOvernightScanJSON(day)
			if !r.Trading {
				notes = append(notes, day+":"+r.Note)
				break
			}
			for i, c := range r.Cands {
				if i >= 5 {
					break
				}
				lines = append(lines, fmt.Sprintf("%s %s 仓%.0f%%", c.Code, c.Name, c.Weight*100))
			}
		case "牛指T+1":
			_, mode := GetAutoPickSettings()
			if mode == "" {
				mode = "标准"
			}
			r := aa.RunNiuZhiScanJSON(day, mode)
			if !r.Trading {
				notes = append(notes, day+":"+r.Note)
				break
			}
			for i, c := range r.Cands {
				if i >= 5 {
					break
				}
				lines = append(lines, fmt.Sprintf("%s %s 评分%d", c.Code, c.Name, c.Score))
			}
		default:
			return "该策略暂不支持历史重演（当前支持：一夜持股/牛指T+1）"
		}
		if len(lines) == 0 {
			empty++
			continue
		}
		msg := aa.CreateTrackBatch(st.ID, day, strings.Join(lines, "\n"))
		logger.SugaredLogger.Infof("补齐 %s %s: %s", strategyName, day, msg)
		created++
	}
	FillAllTrackHoldings()
	out := fmt.Sprintf("补齐完成[%s %s ~ %s]: 新建批次%d个, 无信号日%d个, 已有跳过%d个, 持仓涨幅已回填",
		strategyName, sd.Format("2006-01-02"), ed.Format("2006-01-02"), created, empty, skip)
	if len(notes) > 0 && len(notes) <= 8 {
		out += "\n" + strings.Join(notes, "\n")
	}
	return out
}
