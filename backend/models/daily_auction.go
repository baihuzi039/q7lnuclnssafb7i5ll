package models

import "gorm.io/gorm"

// [local-patch] DailyAuction 集合竞价数据：历史导入（Tushare stk_auction 口径 CSV）
// + 每日 9:26 自动快照抓取。停牌股 price=0 已在导入时过滤。
type DailyAuction struct {
	gorm.Model
	StockCode    string  `json:"stockCode" gorm:"uniqueIndex:idx_auction_code_day;index"` // 000001.SZ
	TradeDate    string  `json:"tradeDate" gorm:"uniqueIndex:idx_auction_code_day;index"` // 2026-11-05
	Vol          float64 `json:"vol"`          // 竞价量（股）
	Price        float64 `json:"price"`        // 竞价匹配价
	Amount       float64 `json:"amount"`       // 竞价额（元）
	PreClose     float64 `json:"preClose"`     // 昨收
	TurnoverRate float64 `json:"turnoverRate"` // 换手率
	VolumeRatio  float64 `json:"volumeRatio"`  // 量比
}

func (s DailyAuction) TableName() string {
	return "daily_auction"
}
