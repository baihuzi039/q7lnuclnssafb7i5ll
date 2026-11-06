package main

// [local-patch] ===== v8-track 策略套件与批次跟踪 =====

import (
	"fmt"
	"os"
	"unicode/utf16"
	"unicode/utf8"
	"path/filepath"
	"sort"
	"strings"
	"time"


	"golang.org/x/text/encoding/simplifiedchinese"

	"go-stock/backend/db"
	"go-stock/backend/logger"
	"go-stock/backend/models"
)

// 预置策略名（首次启动初始化；与用户回测表对应）
var presetTrackStrategies = []string{
	"一夜持股",
	"牛指T+1",
	"竞神接力",
	"古+一日多空",
	"B点七日",
}

// ensurePresetTrackStrategies 首次启动时预置策略套件（已存在则跳过）
func ensurePresetTrackStrategies() {
	var cnt int64
	db.Dao.Model(&models.TrackStrategy{}).Count(&cnt)
	if cnt > 0 {
		return
	}
	for i, name := range presetTrackStrategies {
		db.Dao.Create(&models.TrackStrategy{Name: name, SortOrder: i + 1})
	}
	logger.SugaredLogger.Infof("预置策略套件完成: %v", presetTrackStrategies)
}

// ListTrackStrategies 策略列表
func (a *App) ListTrackStrategies() *[]models.TrackStrategy {
	defer PanicHandler()
	var list []models.TrackStrategy
	db.Dao.Order("sort_order ASC, id ASC").Find(&list)
	return &list
}

// AddTrackStrategy 新增策略套件
func (a *App) AddTrackStrategy(name string) *models.TrackStrategy {
	defer PanicHandler()
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	st := &models.TrackStrategy{Name: name}
	var cnt int64
	db.Dao.Model(&models.TrackStrategy{}).Where("name = ?", name).Count(&cnt)
	if cnt > 0 {
		return nil
	}
	var maxOrder int
	db.Dao.Model(&models.TrackStrategy{}).Select("COALESCE(MAX(sort_order),0)").Scan(&maxOrder)
	st.SortOrder = maxOrder + 1
	if err := db.Dao.Create(st).Error; err != nil {
		logger.SugaredLogger.Errorf("新增策略失败: %v", err)
		return nil
	}
	return st
}

// DelTrackStrategy 删除策略（级联删批次与票，含明细防孤儿数据）
func (a *App) DelTrackStrategy(id uint) string {
	defer PanicHandler()
	var batchIDs []uint
	db.Dao.Model(&models.TrackBatch{}).Where("strategy_id = ?", id).Pluck("id", &batchIDs)
	if len(batchIDs) > 0 {
		db.Dao.Where("batch_id IN ?", batchIDs).Delete(&models.TrackItem{})
	}
	db.Dao.Where("strategy_id = ?", id).Delete(&models.TrackBatch{})
	db.Dao.Where("id = ?", id).Delete(&models.TrackStrategy{})
	return "已删除策略及其全部批次"
}

// GetTrackBatches 某策略的批次列表（新批次在前）
func (a *App) GetTrackBatches(strategyID uint) *[]models.TrackBatch {
	defer PanicHandler()
	var list []models.TrackBatch
	db.Dao.Where("strategy_id = ?", strategyID).Order("trade_date DESC, id DESC").Find(&list)
	return &list
}

// TrackItemRow 批次详情行（含批次信息，前端表格用）
type TrackItemRow struct {
	models.TrackItem
	BatchID   uint   `json:"batchId"`
	TradeDate string `json:"tradeDate"`
	Strategy  string `json:"strategy"`
}

// GetBatchItems 批次内所有票
func (a *App) GetBatchItems(batchID uint) *[]models.TrackItem {
	defer PanicHandler()
	var list []models.TrackItem
	db.Dao.Where("batch_id = ?", batchID).Order("id ASC").Find(&list)
	return &list
}

// GetRecentItems 最近N天的票（跨批次，软件内直接看最近跟踪）
func (a *App) GetRecentTrackItems(strategyID uint, limit int) *[]TrackItemRow {
	defer PanicHandler()
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows []TrackItemRow
	sql := `SELECT i.*, b.trade_date as trade_date, s.name as strategy
		FROM track_items i
		JOIN track_batches b ON i.batch_id = b.id
		JOIN track_strategies s ON b.strategy_id = s.id
		WHERE b.strategy_id = ?
		ORDER BY b.trade_date DESC, i.id ASC LIMIT ?`
	db.Dao.Raw(sql, strategyID, limit).Scan(&rows)
	return &rows
}

// normalizeStockCode 代码归一化：去空格/大写市场后缀，600519 与 600519.SH 等价存储为 600519.SH
func normalizeStockCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return ""
	}
	// 已带后缀
	if strings.HasSuffix(code, ".SH") || strings.HasSuffix(code, ".SZ") || strings.HasSuffix(code, ".BJ") {
		return code
	}
	// 纯6位数字 → 补后缀
	if len(code) == 6 && isAllDigits(code) {
		switch code[0] {
		case '6':
			return code + ".SH"
		case '0', '3':
			return code + ".SZ"
		default: // 4/8开头北交所, 5/1开头基金ETF(不常见于策略)
			return code + ".BJ"
		}
	}
	// sh600519/sz000001/bj833171 前缀形式
	if len(code) == 8 {
		p := code[:2]
		if p == "SH" || p == "SZ" || p == "BJ" {
			return code[2:] + "." + p
		}
	}
	// 无法识别（如整行是名称/其他文本）→ 丢弃，避免垃圾数据入库
	return ""
}

func isAllDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// decodeTxtBytes 自动识别 txt 编码（UTF-8 / GBK / UTF-16）并转为 UTF-8，修复记事本另存为 ANSI 或 Unicode 导致的乱码
func decodeTxtBytes(b []byte) string {
	// BOM 优先
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return string(b[3:])
	}
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE { // UTF-16 LE
		us := make([]uint16, 0, len(b)/2)
		for i := 2; i+1 < len(b); i += 2 {
			us = append(us, uint16(b[i])|uint16(b[i+1])<<8)
		}
		return string(utf16.Decode(us))
	}
	if len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF { // UTF-16 BE
		us := make([]uint16, 0, len(b)/2)
		for i := 2; i+1 < len(b); i += 2 {
			us = append(us, uint16(b[i])<<8|uint16(b[i+1]))
		}
		return string(utf16.Decode(us))
	}
	if utf8.Valid(b) {
		return string(b)
	}
	// 无 BOM：按 GBK/GB18030 解码
	dec, err := simplifiedchinese.GB18030.NewDecoder().Bytes(b)
	if err == nil {
		return string(dec)
	}
	return string(b)
}

// parseTrackTxt 解析txt内容：每行一只票 `代码 [名称] [评分]`，#开头注释跳过
// 返回：代码列表与附加信息
type trackTxtLine struct {
	Code  string
	Name  string
	Score string
}

func parseTrackTxt(content string) []trackTxtLine {
	var out []trackTxtLine
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		// 先按原样解析；首字段识别不出代码且行内有连字符时，兼容 `002086-东方海洋-9.2` 格式重试
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if normalizeStockCode(fields[0]) == "" && strings.Contains(line, "-") {
			parts := strings.SplitN(line, "-", 2)
			rest := ""
			if idx := strings.IndexAny(parts[1], "- \t"); idx >= 0 {
				rest = parts[1][idx+1:]
				parts[1] = parts[1][:idx]
			}
			line = parts[0] + " " + parts[1] + " " + rest
			fields = strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
		}
		code := normalizeStockCode(fields[0])
		if code == "" {
			continue
		}
		row := trackTxtLine{Code: code}
		// 后续字段：纯字母汉字开头的当名称；含数字/小数点的当评分
		for _, f := range fields[1:] {
			if strings.ContainsAny(f, "0123456789.") && f != code {
				if row.Score == "" {
					row.Score = f
				}
			} else if row.Name == "" {
				row.Name = f
			}
		}
		out = append(out, row)
	}
	return out
}

// CreateTrackBatch 手动创建批次（代码列表逗号/换行/空格分隔）
func (a *App) CreateTrackBatch(strategyID uint, tradeDate string, codeList string) string {
	defer PanicHandler()
	tradeDate = strings.TrimSpace(tradeDate)
	if tradeDate == "" {
		tradeDate = time.Now().Format("2006-01-02")
	}
	// 归一化日期
	if d, err := time.Parse("2006-01-02", tradeDate); err == nil {
		tradeDate = d.Format("2006-01-02")
	} else if d, err := time.Parse("20060102", tradeDate); err == nil {
		tradeDate = d.Format("2006-01-02")
	}
	lines := parseTrackTxt(codeList)
	if len(lines) == 0 {
		return "未解析到有效代码，请检查格式：每行一只票（代码 [名称] [评分]）"
	}
	var batch models.TrackBatch
	err := db.Dao.Where("strategy_id = ? AND trade_date = ?", strategyID, tradeDate).First(&batch).Error
	if err != nil {
		batch = models.TrackBatch{StrategyID: strategyID, TradeDate: tradeDate, Source: "manual"}
		if err := db.Dao.Create(&batch).Error; err != nil {
			return "创建批次失败: " + err.Error()
		}
	}
	added := 0
	for _, ln := range lines {
		var cnt int64
		db.Dao.Model(&models.TrackItem{}).Where("batch_id = ? AND stock_code = ?", batch.ID, ln.Code).Count(&cnt)
		if cnt > 0 {
			continue
		}
		item := models.TrackItem{BatchID: batch.ID, StockCode: ln.Code, StockName: ln.Name, Score: ln.Score}
		db.Dao.Create(&item)
		added++
	}
	// 创建后立即回填一次
	FillBatchHoldings(batch.ID)
	return fmt.Sprintf("批次 %s 就绪：新增 %d 只（重复跳过），已回填涨幅", tradeDate, added)
}

// ImportTrackInboxFolder 扫描策略收件箱目录，按 文件名_日期.txt 导入
// 命名：策略名_YYYYMMDD.txt（如 一夜持股_20261107.txt）；策略名须与策略列表一致
func (a *App) ImportTrackInboxFolder() string {
	defer PanicHandler()
	dir := trackInboxDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "收件箱目录不存在: " + dir
	}
	imported, skipped := 0, 0
	var msgs []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".txt") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		idx := strings.LastIndex(base, "_")
		if idx <= 0 {
			skipped++
			continue
		}
		stName := base[:idx]
		datePart := base[idx+1:]
		var st models.TrackStrategy
		if err := db.Dao.Where("name = ?", stName).First(&st).Error; err != nil {
			msgs = append(msgs, "无策略「"+stName+"」，跳过 "+e.Name())
			skipped++
			continue
		}
		var tradeDate string
		if d, err := time.Parse("20060102", datePart); err == nil {
			tradeDate = d.Format("2006-01-02")
		} else if d, err := time.Parse("2006-01-02", datePart); err == nil {
			tradeDate = d.Format("2006-01-02")
		} else {
			msgs = append(msgs, "日期无法解析: "+e.Name())
			skipped++
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			skipped++
			continue
		}
		lines := parseTrackTxt(decodeTxtBytes(content))
		if len(lines) == 0 {
			skipped++
			continue
		}
		var batch models.TrackBatch
		if err := db.Dao.Where("strategy_id = ? AND trade_date = ?", st.ID, tradeDate).First(&batch).Error; err != nil {
			batch = models.TrackBatch{StrategyID: st.ID, TradeDate: tradeDate, Source: "txt"}
			db.Dao.Create(&batch)
		}
		added := 0
		for _, ln := range lines {
			var cnt int64
			db.Dao.Model(&models.TrackItem{}).Where("batch_id = ? AND stock_code = ?", batch.ID, ln.Code).Count(&cnt)
			if cnt > 0 {
				continue
			}
			db.Dao.Create(&models.TrackItem{BatchID: batch.ID, StockCode: ln.Code, StockName: ln.Name, Score: ln.Score})
			added++
		}
		FillBatchHoldings(batch.ID)
		imported++
		msgs = append(msgs, fmt.Sprintf("%s → %s 批次 +%d 只", e.Name(), tradeDate, added))
		// 处理成功的文件挪入 done 子文件夹，避免重复扫描干扰
		doneDir := filepath.Join(dir, "done")
		if mkErr := os.MkdirAll(doneDir, 0755); mkErr == nil {
			os.Rename(filepath.Join(dir, e.Name()), filepath.Join(doneDir, e.Name()))
		}
	}
	sort.Strings(msgs)
	summary := fmt.Sprintf("导入完成：成功 %d 个文件，跳过 %d 个", imported, skipped)
	if len(msgs) > 0 {
		summary += "\n" + strings.Join(msgs, "\n")
	}
	return summary
}

// OpenTrackInboxDir 打开/创建收件箱目录（用户把txt放进来）
func (a *App) OpenTrackInboxDir() string {
	defer PanicHandler()
	dir := trackInboxDir()
	os.MkdirAll(dir, 0755)
	openDir(dir)
	return dir
}

func trackInboxDir() string {
	// 跟随数据库所在目录（data/strategy_inbox），换目录部署后仍一致
	return filepath.Join(filepath.Dir(db.GetDBPath()), "strategy_inbox")
}

// DeleteTrackBatch 删除单个批次及其明细
func (a *App) DeleteTrackBatch(batchID uint) string {
	defer PanicHandler()
	db.Dao.Where("batch_id = ?", batchID).Delete(&models.TrackItem{})
	db.Dao.Where("id = ?", batchID).Delete(&models.TrackBatch{})
	return "已删除批次"
}
