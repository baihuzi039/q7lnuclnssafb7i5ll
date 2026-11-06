package models

import "time"

// [local-patch] ===== v8-track 策略套件与批次跟踪 =====
// 设计要点：
//   策略(TrackStrategy) → 批次(TrackBatch, 一个交易日一批) → 票(TrackItem, 批次内每只票)
//   每套策略独立批次与统计，互不污染；
//   持仓N日涨幅由本地日线库自动回填（Holdings[1..8]）。

// TrackStrategy 策略套件
type TrackStrategy struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	Name      string    `json:"name" gorm:"uniqueIndex;size:64"`          // 策略名（如：一夜持股/牛指T+1/竞神接力/古+一日多空/B点七日）
	Remark    string    `json:"remark" gorm:"size:255"`                          // 备注（选股方式说明）
	SortOrder int       `json:"sortOrder"`                                // 排序
	CreatedAt time.Time `json:"createdAt"`
}

// TrackBatch 一个策略某交易日的批次
type TrackBatch struct {
	ID         uint      `json:"id" gorm:"primaryKey"`
	StrategyID uint      `json:"strategyId" gorm:"index;uniqueIndex:idx_track_batch_unique"`
	TradeDate  string    `json:"tradeDate" gorm:"index;uniqueIndex:idx_track_batch_unique;size:10"` // 2026-11-06
	Source     string    `json:"source" gorm:"size:32"`                                                   // 来源: txt / manual
	Remark     string    `json:"remark" gorm:"size:255"`
	CreatedAt  time.Time `json:"createdAt"`
}

// TrackItem 批次内一只票及其持仓N日跟踪
type TrackItem struct {
	ID         uint    `json:"id" gorm:"primaryKey"`
	BatchID    uint    `json:"batchId" gorm:"index"`
	StockCode  string  `json:"stockCode" gorm:"index;size:16"` // 600519 或 600519.SH
	StockName  string  `json:"stockName" gorm:"size:32"`              // 可空
	Score      string  `json:"score" gorm:"size:32"`                  // 评分/接力分等附加字段（字符串原样存）
	BuyPrice   float64 `json:"buyPrice"`                       // 买入价（可空=用买入日收盘价）
	// 持仓第N日涨幅%：1=买入次日(T+1)，2=第2日...（按交易日序列）
	Hold1Pct float64 `json:"hold1Pct"` // 次日收盘涨幅%
	Hold2Pct float64 `json:"hold2Pct"`
	Hold3Pct float64 `json:"hold3Pct"`
	Hold4Pct float64 `json:"hold4Pct"`
	Hold5Pct float64 `json:"hold5Pct"`
	Hold6Pct float64 `json:"hold6Pct"`
	Hold7Pct float64 `json:"hold7Pct"`
	Hold8Pct float64 `json:"hold8Pct"`
	// 次日细节（对照一夜表：开/高/收）
	NextOpenPct  float64 `json:"nextOpenPct"`  // 次日开盘涨幅%
	NextHighPct  float64 `json:"nextHighPct"`  // 次日最高涨幅%
	NextClosePct float64 `json:"nextClosePct"` // 次日收盘涨幅%
	FilledDays   int     `json:"filledDays"`   // 已回填天数（增量回填用）
	Remark       string  `json:"remark" gorm:"size:255"`
}

// TrackStrategyStat 分策略统计结果（非表，仅返回结构）
type TrackStrategyStat struct {
	StrategyID    uint    `json:"strategyId"`
	StrategyName  string  `json:"strategyName"`
	BatchCount    int64   `json:"batchCount"`
	ItemCount     int64   `json:"itemCount"`
	Hold1WinRate  float64 `json:"hold1WinRate"`  // 次日收盘上涨占比%
	Hold1AvgPct   float64 `json:"hold1AvgPct"`   // 次日收盘平均涨幅%
	NextHighAvg   float64 `json:"nextHighAvg"`   // 次日最高平均涨幅%
	NextHighWin   float64 `json:"nextHighWin"`   // 次日冲高>0占比%
	Hold5AvgPct   float64 `json:"hold5AvgPct"`   // 5日平均收益
	Hold5WinRate  float64 `json:"hold5WinRate"`
}
