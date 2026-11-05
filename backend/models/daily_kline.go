package models

import "gorm.io/gorm"

// [local-patch] DailyKLine 本地日线历史库：供回测/选股器使用，避免每次现拉接口。
// 由每日定时任务增量写入（17:40 工作日），也支持手动触发和外部数据包导入。
type DailyKLine struct {
	gorm.Model
	StockCode string  `json:"stockCode" gorm:"uniqueIndex:idx_daily_kline_code_day;index"`
	StockName string  `json:"stockName"`
	Day       string  `json:"day" gorm:"uniqueIndex:idx_daily_kline_code_day;index"` // 2026-11-05
	Open      float64 `json:"open"`
	Close     float64 `json:"close"`
	High      float64 `json:"high"`
	Low       float64 `json:"low"`
	Volume    float64 `json:"volume"`    // 成交量（股）
	Amount    float64 `json:"amount"`    // 成交额（元）
	PctChange float64 `json:"pctChange"` // 涨跌幅 %
	Turnover  float64 `json:"turnover"`  // 换手率 %
}

func (s DailyKLine) TableName() string {
	return "daily_kline"
}

// DailyKLineSyncStatus 日线数据同步状态（数据管理页展示用）
type DailyKLineSyncStatus struct {
	TotalStocks  int64  `json:"totalStocks"`  // 股票基础表总数
	SyncedStocks int64  `json:"syncedStocks"` // 已有日线的股票数
	TotalRows    int64  `json:"totalRows"`    // 日线总行数
	LatestDay    string `json:"latestDay"`    // 库内最新交易日
	Running      bool   `json:"running"`      // 是否正在同步
	LastSyncTime string `json:"lastSyncTime"` // 上次同步完成时间
}
