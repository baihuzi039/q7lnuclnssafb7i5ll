package main

// [local-patch] app_auction.go — 集合竞价数据：CSV 导入（历史） + 每日 9:26 快照抓取（增量）。
// CSV 口径（Tushare stk_auction）：ts_code,trade_date,vol,price,amount,pre_close,turnover_rate,volume_ratio
// 快照口径：9:26 时刻东财选股接口的 现价(=竞价撮合价)/昨收/量比/换手率，竞价量以成交额/现价近似。

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/duke-git/lancet/v2/convertor"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"go-stock/backend/data"
	"go-stock/backend/db"
	"go-stock/backend/logger"
	"go-stock/backend/models"

	"gorm.io/gorm/clause"
)

// ===== CSV 导入 =====

// ImportAuctionCSV 导入竞价 CSV（可多选文件）
func (a *App) ImportAuctionCSV(files []string) string {
	defer PanicHandler()
	total := 0
	imported := 0
	// 展开目录为文件列表
	var csvFiles []string
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil {
			continue
		}
		if st.IsDir() {
			_ = filepath.Walk(f, func(p string, info os.FileInfo, err error) error {
				if err == nil && !info.IsDir() && strings.HasSuffix(strings.ToLower(p), ".csv") {
					csvFiles = append(csvFiles, p)
				}
				return nil
			})
		} else {
			csvFiles = append(csvFiles, f)
		}
	}
	files = csvFiles
	for _, path := range files {
		n, err := importOneAuctionCSV(path)
		if err != nil {
			logger.SugaredLogger.Errorf("竞价CSV导入失败 %s: %s", path, err.Error())
			continue
		}
		total++
		imported += n
	}
	if total == 0 {
		return "没有可导入的文件"
	}
	msg := fmt.Sprintf("竞价导入完成: %d 个文件, 共写入 %d 条记录", total, imported)
	logger.SugaredLogger.Infof("%s", msg)
	go runtime.EventsEmit(a.ctx, "auctionImportDone", msg)
	return msg
}

func importOneAuctionCSV(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	head, err := r.Read()
	if err != nil {
		return 0, err
	}
	idx := map[string]int{}
	for i, h := range head {
		idx[strings.TrimPrefix(h, "\xEF\xBB\xBF")] = i
	}
	need := []string{"ts_code", "trade_date", "price", "pre_close"}
	for _, k := range need {
		if _, ok := idx[k]; !ok {
			return 0, fmt.Errorf("缺少列 %s", k)
		}
	}
	get := func(row []string, k string) float64 {
		if idx[k] >= len(row) {
			return 0
		}
		v, _ := strconv.ParseFloat(strings.TrimSpace(row[idx[k]]), 64)
		return v
	}
	rows := make([]models.DailyAuction, 0, 6000)
	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}
		code := strings.TrimSpace(row[idx["ts_code"]])
		td := strings.TrimSpace(row[idx["trade_date"]])
		price := get(row, "price")
		if code == "" || td == "" || price <= 0 {
			continue // 停牌股 price=0 过滤
		}
		td = strings.ReplaceAll(td, "-", "")
		if len(td) != 8 {
			continue
		}
		td = td[:4] + "-" + td[4:6] + "-" + td[6:8]
		rows = append(rows, models.DailyAuction{
			StockCode: code, TradeDate: td,
			Vol: get(row, "vol"), Price: price, Amount: get(row, "amount"),
			PreClose: get(row, "pre_close"), TurnoverRate: get(row, "turnover_rate"),
			VolumeRatio: get(row, "volume_ratio"),
		})
	}
	if len(rows) == 0 {
		return 0, nil
	}
	err = db.Dao.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&rows, 1000).Error
	if err != nil {
		return 0, err
	}
	return len(rows), nil
}

// ===== 每日 9:26 快照 =====

// auctionSnapRunning 防止并发抓取
var auctionSnapRunning = false

// SnapshotAuctionNow 抓取当前时刻的竞价/盘前快照（手动补抓按钮也用这个）
func (a *App) SnapshotAuctionNow() string {
	defer PanicHandler()
	if auctionSnapRunning {
		return "竞价快照正在抓取中，请稍候"
	}
	auctionSnapRunning = true
	go func() {
		defer func() { auctionSnapRunning = false }()
		a.doSnapshotAuction()
	}()
	return "竞价快照抓取已启动，全市场约 5~8 分钟完成，完成后本页状态会更新"
}

// doSnapshotAuction 用东财选股接口分页拉全市场快照入库
func (a *App) doSnapshotAuction() {
	defer PanicHandler()
	now := time.Now()
	wd := now.Weekday()
	if wd == time.Saturday || wd == time.Sunday {
		logger.SugaredLogger.Infof("非交易日，跳过竞价快照")
		return
	}
	td := now.Format("2006-01-02")
	var cnt int64
	db.Dao.Model(&models.DailyAuction{}).Where("trade_date = ?", td).Count(&cnt)
	if cnt > 0 {
		logger.SugaredLogger.Infof("今日竞价快照已存在(%d条)，跳过", cnt)
		return
	}
	api := data.NewStockDataApi()
	saved := 0
	for page := 1; page <= 3; page++ {
		res := api.GetAllStocks(page, 3000, "", models.TechnicalIndicators{})
		if res == nil {
			continue
		}
		rows := make([]models.DailyAuction, 0, len(res.Result.Data))
		for _, info := range res.Result.Data {
			price := toF(convertor.ToString(info.NEWPRICE))
			preClose := toF(convertor.ToString(info.PRECLOSEPRICE))
			if price <= 0 || preClose <= 0 {
				continue // 停牌
			}
			amount := toF(convertor.ToString(info.DEALAMOUNT))
			rows = append(rows, models.DailyAuction{
				StockCode: info.SECUCODE, TradeDate: td,
				Price: price, PreClose: preClose,
				Vol:         func() float64 { if price > 0 { return amount / price }; return 0 }(), // 竞价量≈竞价额/竞价价（近似）
				Amount:      amount,
				TurnoverRate: toF(convertor.ToString(info.TURNOVERRATE)),
				VolumeRatio: toF(convertor.ToString(info.VOLUMERATIO)),
			})
		}
		if len(rows) > 0 {
			err := db.Dao.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&rows, 1000).Error
			if err != nil {
				logger.SugaredLogger.Errorf("竞价快照入库失败 page %d: %s", page, err.Error())
			} else {
				saved += len(rows)
			}
		}
	}
	logger.SugaredLogger.Infof("竞价快照完成: %s 共 %d 条", td, saved)
	go runtime.EventsEmit(a.ctx, "auctionSnapshotDone", fmt.Sprintf("%s: %d 条", td, saved))
}

// GetAuctionCount 竞价数据统计（前端展示）
func (a *App) GetAuctionCount() map[string]interface{} {
	defer PanicHandler()
	out := map[string]interface{}{}
	var rows, days int64
	db.Dao.Model(&models.DailyAuction{}).Count(&rows)
	db.Dao.Model(&models.DailyAuction{}).Distinct("trade_date").Count(&days)
	latest := ""
	if rows > 0 {
		db.Dao.Model(&models.DailyAuction{}).Select("MAX(trade_date)").Scan(&latest)
	}
	out["rows"] = rows
	out["days"] = days
	out["latest"] = latest
	return out
}
