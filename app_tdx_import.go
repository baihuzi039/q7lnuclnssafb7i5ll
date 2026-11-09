// [local-patch] v9c.5 (2026-11-09) — 数据管理板块重建：从v5数据层覆盖包v6考古恢复本文件。
package main

// [local-patch] app_tdx_import.go — 通达信 .day 日线数据包导入器。
// 通达信日线格式：每条记录 32 字节 little-endian：
//   [0:4]  uint32 日期 yyyymmdd
//   [4:8]  uint32 开盘价*100
//   [8:12] uint32 最高价*100
//   [12:16] uint32 最低价*100
//   [16:20] uint32 收盘价*100
//   [20:24] uint32 成交额(元)
//   [24:28] uint32 成交量(股)
//   [28:32] 保留
// 文件名如 sh600519.day / sz000001.day / bj830001.day，前缀即交易所。

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"go-stock/backend/db"
	"go-stock/backend/logger"
	"go-stock/backend/models"

	"gorm.io/gorm/clause"
)

// importRunning 防止并发导入
var importRunning atomic.Bool

// ImportTdxDayFolder 导入选定的通达信 .day 文件（前端用文件选择器多选，或选文件夹后端遍历）。
// files: .day 文件的绝对路径列表（也可以是文件夹路径，自动递归收集 .day）
func (a *App) ImportTdxDayFolder(files []string) string {
	if !importRunning.CompareAndSwap(false, true) {
		return "已有导入任务在进行中，请等待完成"
	}
	defer importRunning.Store(false)

	// 展开文件夹
	var dayFiles []string
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil {
			continue
		}
		if st.IsDir() {
			_ = filepath.Walk(f, func(p string, info os.FileInfo, err error) error {
				if err == nil && !info.IsDir() && strings.HasSuffix(strings.ToLower(p), ".day") {
					dayFiles = append(dayFiles, p)
				}
				return nil
			})
		} else if strings.HasSuffix(strings.ToLower(f), ".day") {
			dayFiles = append(dayFiles, f)
		}
	}
	if len(dayFiles) == 0 {
		return "未找到 .day 文件，请选择通达信日线数据文件夹"
	}
	logger.SugaredLogger.Infof("通达信日线导入开始: %d 个文件", len(dayFiles))

	var (
		okFiles atomic.Int64
		rowSum  atomic.Int64
		wg      sync.WaitGroup
		sem     = make(chan struct{}, 8)
	)
	for _, f := range dayFiles {
		wg.Add(1)
		sem <- struct{}{}
		go func(path string) {
			defer wg.Done()
			defer func() { <-sem }()
			n := importOneTdxDay(path)
			if n > 0 {
				okFiles.Add(1)
				rowSum.Add(int64(n))
			}
			runtime.EventsEmit(a.ctx, "tdxImportProgress", map[string]interface{}{
				"done": okFiles.Load(), "total": len(dayFiles), "rows": rowSum.Load(),
			})
		}(f)
	}
	wg.Wait()
	msg := fmt.Sprintf("导入完成: %d/%d 个文件, 共写入 %d 条日线记录", okFiles.Load(), len(dayFiles), rowSum.Load())
	logger.SugaredLogger.Infof("%s", msg)
	runtime.EventsEmit(a.ctx, "tdxImportDone", msg)
	return msg
}

// importOneTdxDay 导入单个 .day 文件，返回写入行数
func importOneTdxDay(path string) int {
	defer PanicHandler()
	base := filepath.Base(path) // sh600519.day
	prefix := strings.ToLower(base[:2])
	code := strings.TrimSuffix(base[2:], ".day")
	if len(code) != 6 {
		logger.SugaredLogger.Errorf("跳过非法文件名: %s", base)
		return 0
	}
	stockCode := code
	switch prefix {
	case "sh":
		stockCode += ".SH"
	case "sz":
		stockCode += ".SZ"
	case "bj":
		stockCode += ".BJ"
	default:
		logger.SugaredLogger.Errorf("未知交易所前缀: %s", base)
		return 0
	}

	raw, err := os.ReadFile(path)
	if err != nil || len(raw) < 32 {
		logger.SugaredLogger.Errorf("读取失败 %s: %v", path, err)
		return 0
	}
	n := len(raw) / 32
	rows := make([]models.DailyKLine, 0, n)
	for i := 0; i+32 <= len(raw); i += 32 {
		rec := raw[i : i+32]
		date := binary.LittleEndian.Uint32(rec[0:4])
		open := float64(binary.LittleEndian.Uint32(rec[4:8])) / 100
		high := float64(binary.LittleEndian.Uint32(rec[8:12])) / 100
		low := float64(binary.LittleEndian.Uint32(rec[12:16])) / 100
		close := float64(binary.LittleEndian.Uint32(rec[16:20])) / 100
		amount := float64(binary.LittleEndian.Uint32(rec[20:24]))
		volume := float64(binary.LittleEndian.Uint32(rec[24:28]))
		if date < 19900101 || close <= 0 {
			continue
		}
		rows = append(rows, models.DailyKLine{
			StockCode: stockCode,
			StockName: "", // 数据包无名称，后续同步或查询时补
			Day:       fmt.Sprintf("%d-%02d-%02d", date/10000, date%10000/100, date%100),
			Open:      open, Close: close, High: high, Low: low,
			Volume: volume, Amount: amount,
		})
	}
	if len(rows) == 0 {
		return 0
	}
	// v9e.7：day文件无涨跌幅字段，按文件内顺序以前一根收盘价现算（首根记0）
	// 通达信.day文件按日期升序存储，rows即升序
	for i := 1; i < len(rows); i++ {
		if rows[i-1].Close > 0 {
			rows[i].PctChange = (rows[i].Close/rows[i-1].Close - 1) * 100
		}
	}
	err = db.Dao.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&rows, 1000).Error
	if err != nil {
		logger.SugaredLogger.Errorf("导入入库失败 %s: %s", base, err.Error())
		return 0
	}
	return len(rows)
}
