package main

// [local-patch] app_backtest.go — 本地回测引擎：读 stock.db 跑 T+1 策略，全程无网络请求。
// 交易规则：信号日收盘确认 → 次日收盘卖出（今买明卖）；成本按佣金万2.5双边+卖出印花税千1。

import (
	"fmt"
	"sort"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"go-stock/backend/db"
	"go-stock/backend/logger"
	"go-stock/backend/models"
)

// BacktestConfig 回测参数
type BacktestConfig struct {
	Strategy   string `json:"strategy"`   // ma_cross / auction_gap / volume_breakout
	StockCode  string `json:"stockCode"`  // 空=全市场（暂限单股，全市场后续开放）
	Days       int    `json:"days"`       // 回测最近 N 根K线
	StartFund  float64 `json:"startFund"` // 初始资金（模拟用）
}

// BacktestTrade 逐笔交易
type BacktestTrade struct {
	StockCode string  `json:"stockCode"`
	StockName string  `json:"stockName"`
	SignalDay string  `json:"signalDay"` // 信号日
	BuyDay    string  `json:"buyDay"`    // 买入日（次日收盘）
	BuyPrice  float64 `json:"buyPrice"`
	SellDay   string  `json:"sellDay"`
	SellPrice float64 `json:"sellPrice"`
	Pct       float64 `json:"pct"` // 净收益率%（扣费后）
}

// BacktestResult 汇总
type BacktestResult struct {
	Strategy    string          `json:"strategy"`
	StockCode   string          `json:"stockCode"`
	TotalTrades int             `json:"totalTrades"`
	Wins        int             `json:"wins"`
	WinRate     float64         `json:"winRate"`   // %
	AvgReturn   float64         `json:"avgReturn"` // %
	MaxReturn   float64         `json:"maxReturn"`
	MinReturn   float64         `json:"minReturn"`
	ProfitLoss  float64         `json:"profitLoss"` // 盈亏比
	TotalReturn float64         `json:"totalReturn"` // 复利累计%
	Trades      []BacktestTrade `json:"trades"`
	CostSeconds float64         `json:"costSeconds"`
}

// RunBacktest 执行回测（前端调用）
func (a *App) RunBacktest(cfg BacktestConfig) *BacktestResult {
	defer PanicHandler()
	start := time.Now()
	res := &BacktestResult{Strategy: cfg.Strategy, StockCode: cfg.StockCode, Trades: []BacktestTrade{}}
	var klines []models.DailyKLine
	q := db.Dao.Model(&models.DailyKLine{}).Order("day ASC")
	if cfg.StockCode != "" {
		q = q.Where("stock_code = ?", cfg.StockCode)
	}
	if cfg.Days <= 0 {
		cfg.Days = 500
	}
	if err := q.Limit(cfg.Days * 2).Find(&klines).Error; err != nil || len(klines) < 30 {
		return res
	}
	// 取最后 N 根
	if len(klines) > cfg.Days {
		klines = klines[len(klines)-cfg.Days:]
	}

	feeBuy := 0.00025 // 佣金万2.5
	feeSell := 0.00025 + 0.001

	sell := func(i int) (float64, bool) { // 第 i 根的次日收盘
		if i+1 < len(klines) {
			return klines[i+1].Close, true
		}
		return 0, false
	}

	fund := 1.0 // 复利模拟
	for i := 20; i < len(klines)-1; i++ {
		var signal bool
		switch cfg.Strategy {
		case "ma_cross": // 5日上穿20日 且 当日涨幅>2%
			if maN(klines, i, 5) > maN(klines, i, 20) && maN(klines, i-1, 5) <= maN(klines, i-1, 20) && klines[i].PctChange > 2 {
				signal = true
			}
		case "auction_gap": // 竞价高开2~7% 且 量比>3（读竞价表）
			var ac models.DailyAuction
			err := db.Dao.Where("stock_code = ? AND trade_date = ?", klines[i].StockCode, klines[i].Day).First(&ac).Error
			if err == nil && ac.PreClose > 0 {
				gap := (ac.Price - ac.PreClose) / ac.PreClose * 100
				if gap >= 2 && gap <= 7 && ac.VolumeRatio > 3 {
					signal = true
				}
			}
		case "volume_breakout": // 量比（当日量/5日均量）>2.5 且 收盘创20日新高
			avgVol := 0.0
			for j := i - 5; j < i; j++ {
				avgVol += klines[j].Volume
			}
			avgVol /= 5
			high20 := 0.0
			for j := i - 20; j < i; j++ {
				if klines[j].High > high20 {
					high20 = klines[j].High
				}
			}
			if avgVol > 0 && klines[i].Volume/avgVol > 2.5 && klines[i].Close > high20 {
				signal = true
			}
		}
		if !signal {
			continue
		}
		sp, ok := sell(i)
		if !ok {
			continue
		}
		bp := klines[i].Close
		pct := (sp*(1-feeSell) - bp*(1+feeBuy)) / (bp * (1 + feeBuy)) * 100
		t := BacktestTrade{
			StockCode: klines[i].StockCode, StockName: klines[i].StockName,
			SignalDay: klines[i].Day, BuyDay: klines[i+1].Day, BuyPrice: bp,
			SellDay: klines[i+1].Day, SellPrice: sp, Pct: pct,
		}
		res.Trades = append(res.Trades, t)
		fund *= (1 + pct/100)
		if pct > 0 {
			res.Wins++
		}
	}
	res.TotalTrades = len(res.Trades)
	if res.TotalTrades > 0 {
		res.WinRate = float64(res.Wins) / float64(res.TotalTrades) * 100
		sum := 0.0
		maxR, minR := -1e9, 1e9
		winSum, lossSum := 0.0, 0.0
		for _, t := range res.Trades {
			sum += t.Pct
			if t.Pct > maxR {
				maxR = t.Pct
			}
			if t.Pct < minR {
				minR = t.Pct
			}
			if t.Pct > 0 {
				winSum += t.Pct
			} else {
				lossSum += -t.Pct
			}
		}
		res.AvgReturn = sum / float64(res.TotalTrades)
		res.MaxReturn = maxR
		res.MinReturn = minR
		if lossSum > 0 {
			res.ProfitLoss = winSum / lossSum
		}
	}
	res.TotalReturn = (fund - 1) * 100
	res.CostSeconds = time.Since(start).Seconds()
	logger.SugaredLogger.Infof("回测完成 %s/%s: %d笔 胜率%.1f%% 平均%.2f%% 耗时%.2fs",
		cfg.Strategy, cfg.StockCode, res.TotalTrades, res.WinRate, res.AvgReturn, res.CostSeconds)
	go runtime.EventsEmit(a.ctx, "backtestDone", res.TotalTrades)
	return res
}

// maN 简单均线（含第 i 根）
func maN(k []models.DailyKLine, i, n int) float64 {
	if i < n-1 {
		return 0
	}
	s := 0.0
	for j := i - n + 1; j <= i; j++ {
		s += k[j].Close
	}
	return s / float64(n)
}
