// [local-patch] v9c.5.2 (2026-11-09) — 数据管理板块升级到v8b最新版：分市场统计（沪深A股/北交所/ETF/异常代码）、同步进度封顶100%防虚高、上次同步高亮chip、竞价记录合并条/天展示。CI编译修复：OpenDirectoryDialog改用OpenDialogOptions结构体（wails v2新版要求）。

package main

// [local-patch] app_daily_kline.go — 本地日线历史库：每日自动增量更新 + 手动触发 + 状态查询。
// 数据源复用 FetchKLineWithFallback（腾讯/东财自动降级），只写本地 SQLite，回测/选股器一律读本地库。

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"go-stock/backend/data"
	"go-stock/backend/db"
	"go-stock/backend/logger"
	"go-stock/backend/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// klineSyncRunning 防止定时任务与手动触发并发同步
var klineSyncRunning atomic.Bool

// klineLastSyncTime 上次同步完成时间（内存态）
// [v9e.4] 同时持久化到 data/kline_last_sync.txt，重启后可读回（原版只在内存，重启显示"尚未同步"）
var klineLastSyncTime atomic.Value // string

const klineLastSyncFile = "data/kline_last_sync.txt"

func klineLastSyncStore(ts string) {
	klineLastSyncTime.Store(ts)
	_ = os.MkdirAll("data", 0o755)
	_ = os.WriteFile(klineLastSyncFile, []byte(ts), 0o644)
}

func klineLastSyncLoad() string {
	if v, ok := klineLastSyncTime.Load().(string); ok && v != "" {
		return v
	}
	if b, err := os.ReadFile(klineLastSyncFile); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

// syncDailyKLineJob 全市场日线增量同步（后台任务）：
//  1. 读取股票基础表（StockBasic，已有每日自动更新机制）拿到全市场代码
//  2. 对每只股票：本地已有"全局最新交易日"数据的跳过；否则拉最近 250 根日K入库（OnConflict DoNothing）
//     v9e.5.1：原为40根，导致首次同步的股票本地只有40根、个股K线查询120根不够（用户反馈根因）
//  3. 4 并发 + 每次请求间隔 50ms，避免触发接口限流
//
// syncFailures 当日内拉取失败票记忆（tsCode -> 失败日期），次日自动清理重试
var syncFailures sync.Map

func syncDailyKLineJob(ctx context.Context) {
	defer PanicHandler()
	if !klineSyncRunning.CompareAndSwap(false, true) {
		logger.SugaredLogger.Infof("日线同步已在进行中，跳过本次触发")
		return
	}
	defer klineSyncRunning.Store(false)
	start := time.Now()

	// [v9f.17.15] 修复同步死锁：全局最新交易日不能只看库内 max(day)——
	// 只要某天没跑同步（软件没开机/断网/任务失败），库内 max 就永远停在那个交易日，
	// 之后每天所有票都被判「已最新」一行不拉，缺数据自我固化（11-12/11-13 全市场缺失的根因）。
	// 先联网校准基准票（最多1次请求），把库内 max 拉回真实最新交易日，再算增量目标。
	for _, cal := range []struct{ code, name string }{
		{"600036.SH", "招商银行"}, {"000001.SZ", "平安银行"}, {"600036", "招商银行"}, {"000001", "平安银行"},
	} {
		if upsertDailyKLine(cal.code, cal.name, 15) > 0 {
			break
		}
	}

	// 全局最新交易日（库内 max(day)），作为增量判断基准
	globalLatest := ""
	if err := db.Dao.Model(&models.DailyKLine{}).Select("MAX(day)").Scan(&globalLatest).Error; err != nil {
		logger.SugaredLogger.Errorf("查询日线最新日期失败: %s", err.Error())
	}
	// [v9f.17.19] 当日内失败票记忆：两个数据源都拉不到的票（北交所无覆盖/退市股/长期停牌），
	// 当天内不再重拉（日志铁证：两轮同步同一批732只重复下载18万行），次日自动失效重试。
	skipFail := 0
	failSkip := map[string]bool{}
	syncFailures.Range(func(k, v any) bool {
		if v.(string) == time.Now().Format("2006-01-02") {
			failSkip[k.(string)] = true
		} else {
			syncFailures.Delete(k)
		}
		return true
	})
	// 各股票已同步到的最新日期
	stockLatest := map[string]string{}
	type latestRow struct {
		StockCode string
		MaxDay    string
	}
	var latestRows []latestRow
	if err := db.Dao.Model(&models.DailyKLine{}).
		Select("stock_code, MAX(day) as max_day").
		Group("stock_code").Find(&latestRows).Error; err == nil {
		for _, r := range latestRows {
			stockLatest[r.StockCode] = r.MaxDay
		}
	}

	var basics []data.StockBasic
	if err := db.Dao.Find(&basics).Error; err != nil {
		logger.SugaredLogger.Errorf("读取股票基础表失败: %s", err.Error())
		return
	}

	// 待同步列表：本地缺失 或 落后于全局最新交易日
	var targets []data.StockBasic
	isEtf := func(symbol string) bool {
		if len(symbol) < 2 {
			return false
		}
		switch symbol[:2] {
		case "15", "16", "50", "51", "52", "53", "56", "58":
			return true
		}
		return false
	}
	// [v9f.17.5] 坏行修复：统计 open=0 的坏行票，强制重拉（不再被“已最新”判定跳过）
	badCodes := map[string]bool{}
	type badRow struct {
		StockCode string
	}
	var badRows []badRow
	if err := db.Dao.Model(&models.DailyKLine{}).
		Where("open = 0 OR close = 0 OR high = 0").
		Select("DISTINCT stock_code").Find(&badRows).Error; err == nil {
		for _, r := range badRows {
			badCodes[r.StockCode] = true
		}
	}
	logger.SugaredLogger.Infof("坏行检测: %d 只票存在 open/close/high=0 的坏行，将强制重拉", len(badCodes))
	for _, b := range basics {
		if isEtf(b.Symbol) {
			continue // ETF/LOF 不拉日线（回测用不到，白耗接口配额）
		}
		if _, isBad := badCodes[b.TsCode]; isBad {
			targets = append(targets, b) // 坏行票强制重拉
			continue
		}
		if failSkip[b.TsCode] {
			skipFail++ // [v9f.17.19] 当日内已确认拉不到，跳过
			continue
		}
		if d, ok := stockLatest[b.TsCode]; ok && globalLatest != "" {
			// [v9f.17.19] 达到全局最新 或 次新交易日（停牌/无当天K线）即视为已同步
			if d >= globalLatest {
				continue
			}
			if failSkip[b.TsCode] {
				skipFail++
				continue
			}
		}
		targets = append(targets, b)
	}
	logger.SugaredLogger.Infof("日线同步开始: 共 %d 只, 待同步 %d 只, 当日失败跳过 %d 只 (全局最新 %s)", len(basics), len(targets), skipFail, globalLatest)

	var (
		wg        sync.WaitGroup
		sem       = make(chan struct{}, 4)
		okCount   atomic.Int64
		rowSum    atomic.Int64
		failCount atomic.Int64
	)
	// [v9f.17.18] 连续失败熔断：网络被断（EOF风暴）时每票耗尽全部候选超时，
	// 几百票能卡几小时且永不结束，回填在启动链里被堵在后面。连续20票全失败判定网络不可用，中止本轮同步。
	var onGoingFail atomic.Int64
	var aborted atomic.Bool
	for _, b := range targets {
		if aborted.Load() {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(code, name string) {
			defer wg.Done()
			defer func() { <-sem }()
			if aborted.Load() {
				return
			}
			n := upsertDailyKLine(code, name, 250)
			if n > 0 {
				okCount.Add(1)
				rowSum.Add(int64(n))
				onGoingFail.Store(0)
			} else {
				failCount.Add(1)
				syncFailures.Store(code, time.Now().Format("2006-01-02")) // [v9f.17.19] 记入当日失败集
				if onGoingFail.Add(1) >= 20 {
					aborted.Store(true)
					logger.SugaredLogger.Errorf("日线同步熔断：连续%d只拉取失败，判定网络不可用，中止本轮，待恢复后重试", onGoingFail.Load())
				}
			}
			time.Sleep(50 * time.Millisecond) // 控制请求频率
		}(b.TsCode, b.Name)
	}
	wg.Wait()
	if aborted.Load() {
		logger.SugaredLogger.Infof("日线同步中止: 成功 %d 只, 失败/停牌 %d 只, 新增 %d 行, 耗时 %s",
			okCount.Load(), failCount.Load(), rowSum.Load(), time.Since(start).Round(time.Second))
		return
	}
	klineLastSyncStore(time.Now().Format("2006-01-02 15:04:05"))
	InvalidateKlineStatusCache() // [v9f.3] 同步完成强刷状态缓存
	logger.SugaredLogger.Infof("日线同步完成: 成功 %d 只, 失败/停牌 %d 只, 新增 %d 行, 耗时 %s",
		okCount.Load(), failCount.Load(), rowSum.Load(), time.Since(start).Round(time.Second))
}

// upsertDailyKLine 拉取单只股票日K并入库，返回新增行数
// v9e.8：优先走东财K线源（免费、自带换手率+涨跌幅，修复MAC源无换手率导致 turnover 全 0）；失败再退回原降级链
func upsertDailyKLine(stockCode, stockName string, limit int) int {
	defer PanicHandler()
	result := data.FetchFromEastMoneyKLine(stockCode, stockName, "101", limit, "")
	if result == nil || result.Data == nil || len(*result.Data) == 0 {
		result = data.FetchKLineWithFallback(stockCode, stockName, "101", limit, "")
	}
	if result == nil || result.Data == nil || len(*result.Data) == 0 {
		return 0
	}
	rows := make([]models.DailyKLine, 0, len(*result.Data))
	// [v9f.11] 盘中同步时丢弃当日未收盘的"半根K线"，保证库里数据完整准确：
	// 当日 15:00 收盘前，东财返回的当日 bar 是实时进行中数据（close=最新价），写入会污染回填/选股。
	// 已收盘（15:00 后）则正常保留当日 bar。
	today := time.Now().Format("2006-01-02")
	marketClosed := time.Now().After(time.Date(time.Now().Year(), time.Now().Month(), time.Now().Day(), 15, 0, 0, 0, time.Now().Location()))
	for _, k := range *result.Data {
		if k.Day == today && !marketClosed {
			continue // 盘中：跳过当日未收盘数据
		}
		rows = append(rows, models.DailyKLine{
			StockCode: stockCode,
			StockName: stockName,
			Day:       k.Day,
			Open:      toF(k.Open),
			Close:     toF(k.Close),
			High:      toF(k.High),
			Low:       toF(k.Low),
			Volume:    toF(k.Volume),
			Amount:    toF(k.Amount),
			PctChange: toF(k.ChangePercent),
			Turnover:  toF(k.TurnoverRate),
		})
	}
	// v9e.9.2：已有行不再跳过，补写 涨跌幅/换手率；[v9f] 扩为 open/close/pct_change/turnover 四列
	// （治历史坏行：open=0 导致策略跟踪"次日开盘"空缺，v9f 起同步顺带补齐；0值仍不更新保护已有值）
	err := db.Dao.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "stock_code"}, {Name: "day"}},
		// [v9f.17.5] 补齐 high/low/volume/amount：原只更新4列，历史坏行的 high=0 永远修不上（次日高空的直接原因）
		DoUpdates: clause.AssignmentColumns([]string{"open", "close", "high", "low", "volume", "amount", "pct_change", "turnover"}),
		Where:     clause.Where{Exprs: []clause.Expression{gorm.Expr("excluded.open <> 0 OR excluded.close <> 0 OR excluded.pct_change <> 0 OR excluded.turnover <> 0")}},
	}).CreateInBatches(&rows, 500).Error
	if err != nil {
		logger.SugaredLogger.Errorf("日线入库失败 %s(%s): %s", stockName, stockCode, err.Error())
		return 0
	}
	return len(rows)
}

// toF 字符串数值转 float64，解析失败返回 0
func toF(s string) float64 {
	var f float64
	_, _ = fmt.Sscanf(s, "%f", &f)
	return f
}

// ===== 以下为前端可调用的 Wails 绑定方法 =====

// ExportDatabaseBackup 导出 stock.db 备份到指定文件夹（WAL 收拢后整文件拷贝，不中断服务）
func (a *App) ExportDatabaseBackup() string {
	defer PanicHandler()
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: "选择备份保存位置"})
	if err != nil || dir == "" {
		return ""
	}
	dbPath := db.GetDBPath()
	if _, err := os.Stat(dbPath); err != nil {
		return "数据库文件不存在: " + dbPath
	}
	// 先执行 checkpoint 把 WAL 内容并回主库，保证拷出的单文件是完整的
	if err := db.Dao.Exec("PRAGMA wal_checkpoint(TRUNCATE)").Error; err != nil {
		logger.SugaredLogger.Errorf("WAL checkpoint 失败: %s", err.Error())
	}
	stamp := time.Now().Format("20060102_150405")
	dest := filepath.Join(dir, fmt.Sprintf("stock_backup_%s.db", stamp))
	src, err := os.Open(dbPath)
	if err != nil {
		return "打开数据库失败: " + err.Error()
	}
	defer src.Close()
	dst, err := os.Create(dest)
	if err != nil {
		return "创建备份文件失败: " + err.Error()
	}
	defer dst.Close()
	if _, err := io.Copy(dst, src); err != nil {
		return "备份写入失败: " + err.Error()
	}
	msg := fmt.Sprintf("备份完成: %s (%.1f MB)", dest, float64(fileSize(dest))/1024/1024)
	logger.SugaredLogger.Infof("%s", msg)
	return msg
}

func fileSize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size()
}

// SelectDirectory 弹出系统文件夹选择框
func (a *App) SelectDirectory() string {
	defer PanicHandler()
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: "选择通达信日线数据文件夹"})
	if err != nil || dir == "" {
		return ""
	}
	return dir
}

// SyncDailyKLineNow 手动触发一次全市场日线同步（异步执行，立即返回提示）
func (a *App) SyncDailyKLineNow() string {
	if !klineSyncRunning.CompareAndSwap(false, true) {
		return "日线同步正在进行中，请稍后再试"
	}
	klineSyncRunning.Store(false)
	go func() {
		syncDailyKLineJob(a.ctx) // job 内部有原子锁防并发
		runtime.EventsEmit(a.ctx, "dailyKLineSyncDone", time.Now().Format("2006-01-02 15:04:05"))
	}()
	return "日线同步已启动，全市场首次约需 10-20 分钟，可在本页刷新状态查看进度"
}

// GetDailyKLineSyncStatus 查询日线库状态（分类计数：A股股票/ETF/其他，避免把基金债券算进股票数）
// [v9f.3] 切页卡顿优化：重聚合部分用60秒缓存（Running/LastSyncTime 始终实时）；
// 同步完成/导入完成事件调 InvalidateKlineStatusCache 强刷
// KlineHealthCheck [v9f.17.8] 数据体检：总行数/股票数/日期范围/坏行数，直接返回报告文本（前端弹窗显示）
func (a *App) KlineHealthCheck() string {
	defer PanicHandler()
	var total, codes, bad int64
	var minDay, maxDay string
	db.Dao.Model(&models.DailyKLine{}).Count(&total)
	db.Dao.Model(&models.DailyKLine{}).Distinct("stock_code").Count(&codes)
	db.Dao.Model(&models.DailyKLine{}).Select("MIN(day), MAX(day)").Row().Scan(&minDay, &maxDay)
	db.Dao.Model(&models.DailyKLine{}).Where("open = 0 OR close = 0 OR high = 0").Count(&bad)
	return fmt.Sprintf(
		"数据库体检结果：\n总行数: %d\n股票数: %d\n日期范围: %s ~ %s\n坏行(open/close/high为0): %d\n\n%s",
		total, codes, minDay, maxDay, bad,
		map[bool]string{true: "✔ 库健康，之前缺数据是回填逻辑问题，v9f.17.7 已修，点刷新即可补全", false: "⚠ 有坏行：点「接口同步增量」，同步会自动强制重拉这些票，完成后重新体检应归零"}[bad == 0])
}

func (a *App) GetDailyKLineSyncStatus() models.DailyKLineSyncStatus {
	defer PanicHandler()
	now := time.Now().Unix()
	cached := klineStatusCache.Load()
	if cached != nil && now-klineStatusCacheAt.Load() < 60 {
		c := cached.(models.DailyKLineSyncStatus)
		c.Running = klineSyncRunning.Load()
		if v := klineLastSyncLoad(); v != "" {
			c.LastSyncTime = v
		}
		return c
	}
	st := computeDailyKLineSyncStatus()
	klineStatusCache.Store(st)
	klineStatusCacheAt.Store(now)
	st.Running = klineSyncRunning.Load()
	if v := klineLastSyncLoad(); v != "" {
		st.LastSyncTime = v
	}
	return st
}

// klineStatusCache 日线状态缓存（[v9f.3] 60秒过期；事件强刷）
var (
	klineStatusCache   atomic.Value
	klineStatusCacheAt atomic.Int64 // unix秒
)

// InvalidateKlineStatusCache 强制失效状态缓存（同步完成/导入完成后调用）
func InvalidateKlineStatusCache() {
	klineStatusCacheAt.Store(0)
}

// computeDailyKLineSyncStatus 实际执行聚合查询（原 GetDailyKLineSyncStatus 主体）
func computeDailyKLineSyncStatus() models.DailyKLineSyncStatus {
	var st models.DailyKLineSyncStatus
	if v := klineLastSyncLoad(); v != "" {
		st.LastSyncTime = v
	}
	// 分类统计基础表：A股股票(60/00/30/68/8/4开头，排除ETF 51/56/58/15/16/50等) vs ETF
	type cnt struct {
		Category string
		N        int64
	}
	var cs []cnt
	// ts_code 形如 600519.SH / 000001.SZ / 833171.BJ / 510300.SH(ETF)，
	// symbol 字段在部分历史数据里可能为空，故统一用 ts_code 前缀判断；
	// 已退市股（list_status=D/P）不纳入分类展示，避免干扰总数
	db.Dao.Raw(`SELECT CASE
		WHEN substr(ts_code,1,2) IN ('51','52','53','56','58','15','16','50','11','12','13') THEN 'etf'
		WHEN substr(ts_code,1,2) IN ('60','00','30','68') THEN 'astock'
		WHEN substr(ts_code,1,1) IN ('8','4') THEN 'bj'
		ELSE 'other' END as category, count(*) as n
		FROM tushare_stock_basic
		WHERE list_status = 'L' OR list_status = '' OR list_status IS NULL
		GROUP BY category`).Scan(&cs)
	for _, c := range cs {
		switch c.Category {
		case "astock":
			st.TotalAStock = c.N
		case "etf":
			st.TotalEtf = c.N
		case "bj":
			st.TotalBJ = c.N
		case "other":
			st.TotalOther = c.N
		}
	}
	st.TotalStocks = st.TotalAStock + st.TotalBJ // 进度分母只算沪深A股+北交所股票
	// ETF与日线无关，不参与日线同步，不计入分母；other为异常数据展示用
	db.Dao.Model(&models.DailyKLine{}).Count(&st.TotalRows)
	db.Dao.Model(&models.DailyKLine{}).Distinct("stock_code").Count(&st.SyncedStocks)
	if st.TotalRows > 0 {
		db.Dao.Model(&models.DailyKLine{}).Select("MAX(day)").Scan(&st.LatestDay)
	}
	return st
}

// FetchSingleStockKLine 联网补抓单只股票日K入库（v9e.5 新增，解决本地K线数据不全）
// 返回新增行数；days<=0 默认 250 个交易日
func (a *App) FetchSingleStockKLine(stockCode string, days int) int {
	defer PanicHandler()
	if days <= 0 {
		days = 250
	}
	var name string
	db.Dao.Raw(`SELECT name FROM tushare_stock_basic WHERE ts_code = ?`, stockCode).Scan(&name)
	if name == "" {
		name = stockCode
	}
	n := upsertDailyKLine(stockCode, name, days)
	logger.SugaredLogger.Infof("单股补抓 %s(%s): 新增 %d 根日K", stockCode, name, n)
	return n
}

// GetLocalDailyKLine 读取本地日线（回测/展示用，不发任何网络请求）
func (a *App) GetLocalDailyKLine(stockCode string, days int) *[]models.DailyKLine {
	defer PanicHandler()
	if days <= 0 {
		days = 250
	}
	var rows []models.DailyKLine
	err := db.Dao.Where("stock_code = ?", stockCode).
		Order("day DESC").Limit(days).Find(&rows).Error
	if err != nil {
		logger.SugaredLogger.Errorf("读取本地日线失败 %s: %s", stockCode, err.Error())
		return &[]models.DailyKLine{}
	}
	// 反转为时间升序返回
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return &rows
}

// [local-patch] ===== 分类导出 CSV =====

// ExportReq 导出请求参数
type ExportReq struct {
	DataType  string `json:"dataType"`  // daily/auction/follow/recommend/backtest
	StockCode string `json:"stockCode"` // 空=全部
	StartDate string `json:"startDate"` // 2006-01-02，空=不限
	EndDate   string `json:"endDate"`   // 2006-01-02，空=不限
}

// ExportDataCSV 按类别导出 CSV 到用户选择的文件夹
func (a *App) ExportDataCSV(req ExportReq) string {
	defer PanicHandler()
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: "选择导出保存位置"})
	if err != nil || dir == "" {
		return ""
	}
	var header []string
	var rows [][]string
	switch req.DataType {
	case "daily":
		header = []string{"stock_code", "stock_name", "day", "open", "close", "high", "low", "volume", "amount", "pct_change", "turnover"}
		q := db.Dao.Model(&models.DailyKLine{})
		if req.StockCode != "" {
			q = q.Where("stock_code = ?", req.StockCode)
		}
		if req.StartDate != "" {
			q = q.Where("day >= ?", req.StartDate)
		}
		if req.EndDate != "" {
			q = q.Where("day <= ?", req.EndDate)
		}
		var list []models.DailyKLine
		if err := q.Order("stock_code, day").Limit(3000000).Find(&list).Error; err != nil {
			return "查询失败: " + err.Error()
		}
		for _, r := range list {
			rows = append(rows, []string{r.StockCode, r.StockName, r.Day,
				fmt.Sprintf("%g", r.Open), fmt.Sprintf("%g", r.Close), fmt.Sprintf("%g", r.High),
				fmt.Sprintf("%g", r.Low), fmt.Sprintf("%g", r.Volume), fmt.Sprintf("%g", r.Amount),
				fmt.Sprintf("%g", r.PctChange), fmt.Sprintf("%g", r.Turnover)})
		}
	case "auction":
		return "竞价数据尚未入库（后续版本可用）"
	case "follow":
		header = []string{"stock_code", "stock_name", "cost_price", "created_at"}
		var list []data.FollowedStock
		if err := db.Dao.Find(&list).Error; err != nil {
			return "查询失败: " + err.Error()
		}
		for _, r := range list {
			rows = append(rows, []string{r.StockCode, r.Name, fmt.Sprintf("%g", r.CostPrice), r.Time.Format("2006-01-02 15:04:05")})
		}
	case "recommend":
		header = []string{"stock_code", "stock_name", "rating", "price", "recommend_time"}
		var list []models.AiRecommendStocks
		if err := db.Dao.Find(&list).Error; err != nil {
			return "查询失败: " + err.Error()
		}
		for _, r := range list {
			t := ""
			if r.DataTime != nil {
				t = r.DataTime.Format("2006-01-02 15:04:05")
			}
			rows = append(rows, []string{r.StockCode, r.StockName, r.Rating, r.StockPrice, t})
		}
	case "backtest":
		header = []string{"stock_code", "stock_name", "rating", "recommend_time", "recommend_price", "period_days", "return_pct", "benchmark_pct", "excess_pct", "outcome"}
		var list []models.AiRecommendBacktest
		if err := db.Dao.Find(&list).Error; err != nil {
			return "查询失败: " + err.Error()
		}
		for _, r := range list {
			rows = append(rows, []string{r.StockCode, r.StockName, r.Rating,
				r.RecommendTime.Format("2006-01-02"), fmt.Sprintf("%g", r.RecommendPrice),
				fmt.Sprintf("%d", r.PeriodDays), fmt.Sprintf("%g", r.ReturnPct),
				fmt.Sprintf("%g", r.BenchmarkPct), fmt.Sprintf("%g", r.ExcessPct), r.Outcome})
		}
	default:
		return "未知类别: " + req.DataType
	}
	if len(rows) == 0 {
		return "该条件下无数据"
	}
	nameMap := map[string]string{"daily": "日线", "auction": "竞价", "follow": "自选股", "recommend": "AI推荐", "backtest": "回测结果"}
	fname := nameMap[req.DataType]
	if req.StockCode != "" {
		fname += "_" + req.StockCode
	}
	if req.StartDate != "" || req.EndDate != "" {
		fname += "_" + req.StartDate + "_" + req.EndDate
	}
	fname += "_" + time.Now().Format("20060102_150405") + ".csv"
	dest := filepath.Join(dir, fname)
	f, err := os.Create(dest)
	if err != nil {
		return "创建文件失败: " + err.Error()
	}
	defer f.Close()
	_, _ = f.WriteString("\xEF\xBB\xBF") // BOM: Excel 打开中文不乱码
	w := csv.NewWriter(f)
	_ = w.Write(header)
	for _, r := range rows {
		_ = w.Write(r)
	}
	w.Flush()
	msg := fmt.Sprintf("导出完成: %s (%d 行)", dest, len(rows))
	logger.SugaredLogger.Infof("%s", msg)
	return msg
}
