package main

// [local-patch] ===== v8-track 策略套件与批次跟踪 =====

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
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
		// v9b.3: 老库回填 Builtin 标记
		db.Dao.Model(&models.TrackStrategy{}).Where("name IN ?", presetTrackStrategies).Update("builtin", true)
		return
	}
	for i, name := range presetTrackStrategies {
		db.Dao.Create(&models.TrackStrategy{Name: name, SortOrder: i + 1, Builtin: true})
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
// RenameTrackStrategy 重命名策略（v9b.3: 仅自建策略可改名；内置预置策略禁止）
func (a *App) RenameTrackStrategy(id uint, name string) string {
	defer PanicHandler()
	name = strings.TrimSpace(name)
	if name == "" {
		return "名称不能为空"
	}
	var st models.TrackStrategy
	if err := db.Dao.First(&st, id).Error; err != nil {
		return "策略不存在"
	}
	if st.Builtin {
		return "内置策略不可重命名"
	}
	for _, p := range presetTrackStrategies {
		if st.Name == p {
			return "内置策略不可重命名"
		}
	}
	var cnt int64
	db.Dao.Model(&models.TrackStrategy{}).Where("name = ? AND id != ?", name, id).Count(&cnt)
	if cnt > 0 {
		return "已存在同名策略"
	}
	if err := db.Dao.Model(&st).Update("name", name).Error; err != nil {
		return "改名失败: " + err.Error()
	}
	return "OK"
}

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
// [v9f] 附带 itemCount（明细数），前端导出跳过空批次用
func (a *App) GetTrackBatches(strategyID uint) *[]map[string]interface{} {
	defer PanicHandler()
	var list []models.TrackBatch
	db.Dao.Where("strategy_id = ?", strategyID).Order("trade_date DESC, id DESC").Find(&list)
	out := make([]map[string]interface{}, 0, len(list))
	for _, b := range list {
		var cnt int64
		db.Dao.Model(&models.TrackItem{}).Where("batch_id = ?", b.ID).Count(&cnt)
		out = append(out, map[string]interface{}{
			"id": b.ID, "strategyId": b.StrategyID, "tradeDate": b.TradeDate,
			"mode":   b.Mode,
			"source": b.Source, "remark": b.Remark, "createdAt": b.CreatedAt,
			"itemCount": cnt,
		})
	}
	return &out
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

// GetRecentTrackItemsMode [v9f.17.16] 按模式过滤的近期明细：mode 为空=不过滤（通用批次），
// 牛指T+1 传 标准/严选/全勤 只取对应模式批次，三模式数据不再混在同一张表里
func (a *App) GetRecentTrackItemsMode(strategyID uint, mode string, limit int) *[]TrackItemRow {
	defer PanicHandler()
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows []TrackItemRow
	sql := `SELECT i.*, b.trade_date as trade_date, s.name as strategy
		FROM track_items i
		JOIN track_batches b ON i.batch_id = b.id
		JOIN track_strategies s ON b.strategy_id = s.id
		WHERE b.strategy_id = ?`
	args := []interface{}{strategyID}
	if mode != "" {
		// [v9f.17.16] 兼容老数据：17.15 及之前的牛指批次 mode 为空，视为"标准"
		if mode == "标准" {
			sql += " AND (b.mode = '标准' OR b.mode = '')"
		} else {
			sql += " AND b.mode = ?"
			args = append(args, mode)
		}
	}
	sql += " ORDER BY b.trade_date DESC, i.id ASC LIMIT ?"
	args = append(args, limit)
	db.Dao.Raw(sql, args...).Scan(&rows)
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

// trackTxtLine 单行解析结果：归一化后的代码/名称/评分原文
type trackTxtLine struct {
	Code  string
	Name  string
	Score string
	Mark  string // [v9f.17.1] 标记词（严/★等）
}

// parseTrackTxt 智能解析：以识别代码为主，分隔符/字段顺序全兼容。
// 支持：空格/Tab/逗号（中英文）/顿号/分号/竖线/连字符 分隔；名称、评分、代码任意顺序；
// 评分支持 9.2、92分、评分92 等写法；名称自动去除引号/括号包裹；带行号自动去行号。
func parseTrackTxt(content string) []trackTxtLine {
	var out []trackTxtLine
	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(rawLine)
		// 去行号（如 "1. 600519.SH ..." 或 "1、600519.SH ..."）；行号后必须紧跟空白，避免误吃 "9.2" 开头的评分
		if idx := strings.IndexAny(line, ".、．"); idx > 0 && idx <= 4 {
			prefix := line[:idx]
			sep := line[idx]
			// '.'作小数点的情况（如"9.2"）：后面必须跟空白才算行号；'、''．'是明确行号分隔
			ok := sep != '.' || (idx+1 < len(line) && (line[idx+1] == ' ' || line[idx+1] == '\t'))
			if isAllDigits(prefix) && ok {
				line = strings.TrimSpace(line[idx+1:])
			}
		}
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		// 统一分隔符：全角逗号/顿号/分号/竖线/制表符/英文逗号 → 空格
		for _, s := range []string{"，", "、", "；", "；", "｜", "|", ";", "\t", ",", "\u3000"} {
			line = strings.ReplaceAll(line, s, " ")
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		// 逐词归类：代码优先识别，评分次之，标记词（严/★）存Remark，其余当名称（顺序无关）
		var code, name, score, mark string
		isMark := func(f string) bool { return f == "严" || f == "★" || f == "重" || f == "轻" }
		for _, f := range fields {
			f = strings.Trim(f, "\"'‘’“”()（）[]【】")
			if f == "" {
				continue
			}
			// 1) 代码（含 002086-东方海洋 连字符词）
			if code == "" {
				if c, rest := splitCodeWord(f); c != "" {
					code = c
					if rest != "" && name == "" {
						name = rest
					}
					continue
				}
			}
			// 2) 标记词（严/★等）
			if isMark(f) {
				if mark == "" {
					mark = f
				}
				continue
			}
			// 3) 评分（支持 9.2 / 92分 / 评分92）
			if score == "" {
				s := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSuffix(f, "分"), "评分"), "分")
				if s != f && isScoreLike(s) {
					score = s
					continue
				}
				if s == f && isScoreLike(s) && !looksLikeCode(s) && len(fields) > 1 {
					// 裸数字评分（如 "600519.SH 贵州茅台 9.2" 的 9.2）
					score = s
					continue
				}
			}
			// 4) 名称
			if name == "" {
				name = f
			}
		}
		if code == "" {
			continue // 无法识别代码的行丢弃，不入库
		}
		out = append(out, trackTxtLine{Code: code, Name: name, Score: score, Mark: mark})
	}
	return out
}

// splitCodeWord 尝试从词中提取代码；连字符词（002086-东方海洋）返回代码与剩余名称
func splitCodeWord(f string) (string, string) {
	if looksLikeCode(f) {
		return normalizeStockCode(f), ""
	}
	if idx := strings.IndexAny(f, "-—"); idx > 0 {
		first, rest := f[:idx], f[idx+1:]
		if looksLikeCode(first) {
			return normalizeStockCode(first), strings.Trim(rest, "\"'‘’“”()（）[]【】")
		}
	}
	return "", ""
}

// looksLikeCode 判断是否像股票代码：6位数字 / 600519.SH / sh600519
func looksLikeCode(f string) bool {
	u := strings.ToUpper(f)
	if len(u) == 6 && isAllDigits(u) {
		return true
	}
	if len(u) == 9 && isAllDigits(u[:6]) {
		suf := u[7:]
		if (suf == "SH" || suf == "SZ" || suf == "BJ") && u[6] == '.' {
			return true
		}
	}
	if len(u) == 8 && isAllDigits(u[2:]) {
		p := u[:2]
		if p == "SH" || p == "SZ" || p == "BJ" {
			return true
		}
	}
	return false
}

// isScoreLike 评分样式：1-5位数字或带1个小数点（9.2 / 92 / 9.25）
func isScoreLike(s string) bool {
	if s == "" || len(s) > 5 {
		return false
	}
	if strings.Count(s, ".") > 1 {
		return false
	}
	return isAllDigits(strings.ReplaceAll(s, ".", ""))
}

// CreateTrackBatch 手动创建批次（代码列表逗号/换行/空格分隔）
func (a *App) CreateTrackBatch(strategyID uint, tradeDate string, codeList string) string {
	return a.CreateTrackBatchMode(strategyID, tradeDate, codeList, "")
}

// CreateTrackBatchMode [v9f.17.16] 带模式建批次：牛指T+1 标准/严选/全勤 各自独立批次；
// mode 为空=通用批次（一夜持股等）。同策略+同日期+同模式幂等。
func (a *App) CreateTrackBatchMode(strategyID uint, tradeDate string, codeList string, mode string) string {
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
	err := db.Dao.Where("strategy_id = ? AND trade_date = ? AND mode = ?", strategyID, tradeDate, mode).First(&batch).Error
	if err != nil {
		batch = models.TrackBatch{StrategyID: strategyID, TradeDate: tradeDate, Mode: mode, Source: "manual"}
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
		item := models.TrackItem{BatchID: batch.ID, StockCode: ln.Code, StockName: ln.Name, Score: ln.Score, Remark: ln.Mark}
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
	found := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".txt") {
			continue
		}
		found++
		base := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		idx := strings.LastIndex(base, "_")
		if idx <= 0 {
			skipped++
			continue
		}
		stName := base[:idx]
		datePart := base[idx+1:]
		var st models.TrackStrategy
		if err := db.Dao.Where("name = ?", strings.TrimSpace(stName)).First(&st).Error; err != nil {
			// 列出实际存在的策略名，帮助定位是名字不匹配还是绑定未生效
			var names []string
			db.Dao.Model(&models.TrackStrategy{}).Pluck("name", &names)
			msgs = append(msgs, "无策略「"+stName+"」，跳过 "+e.Name()+"（现有策略: "+strings.Join(names, "、")+"）")
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
			msgs = append(msgs, e.Name()+" 未解析出任何股票（内容不是可识别的 代码/名称/评分 格式）")
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
	if found == 0 {
		summary := fmt.Sprintf("收件箱里没有txt文件（扫描路径: %s）。请把文件放到这个目录；已成功导入过的文件会被挪入其中的 done 子文件夹", dir)
		if len(msgs) > 0 {
			summary += "\n" + strings.Join(msgs, "\n")
		}
		return summary
	}
	summary := fmt.Sprintf("导入完成：找到 %d 个txt，成功 %d，跳过 %d（扫描路径: %s）", found, imported, skipped, dir)
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

// DeleteTrackItem 删除单条明细
func (a *App) DeleteTrackItem(itemID uint) string {
	defer PanicHandler()
	db.Dao.Where("id = ?", itemID).Delete(&models.TrackItem{})
	return "已删除"
}

// DeleteTrackBatch 删除单个批次及其明细
func (a *App) DeleteTrackBatch(batchID uint) string {
	defer PanicHandler()
	var batch models.TrackBatch
	if err := db.Dao.First(&batch, batchID).Error; err == nil {
		var st models.TrackStrategy
		if err := db.Dao.First(&st, batch.StrategyID).Error; err == nil {
			sheet := sheetPathFor(st.Name) // [v9f.17.1] 同步从回测表清除该批次所有行
			if f, err := excelize.OpenFile(sheet); err == nil {
				sheetName := "五年明细"
				if st.Name == "牛指T+1" {
					sheetName = "近五年回测明细"
				}
				rows, _ := f.GetRows(sheetName)
				removed := 0
				for i := len(rows) - 1; i >= 1; i-- { // 倒序删避免行号位移
					r := rows[i]
					if len(r) >= 2 && strings.TrimSpace(r[0]) == batch.TradeDate {
						f.RemoveRow(sheetName, i+1)
						removed++
					}
				}
				if removed > 0 {
					f.Save()
					logger.SugaredLogger.Infof("删除批次 %s 同步清除回测表 %d 行", batch.TradeDate, removed)
				}
				f.Close()
			}
		}
	}
	db.Dao.Where("batch_id = ?", batchID).Delete(&models.TrackItem{})
	db.Dao.Where("id = ?", batchID).Delete(&models.TrackBatch{})
	return "已删除批次（回测表对应行已同步清除）"
}
