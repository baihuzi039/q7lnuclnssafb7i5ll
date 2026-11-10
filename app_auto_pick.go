package main

// ==================================================================
// [v9e] 14:50 自动选股（两引擎制度闸门已过：一夜453只100%重合 / 牛指长窗口8基准日一致+2日判责基准数据缺）
// 流程: 工作日14:50 cron → 各按设置模式跑一夜+牛指引擎 → 写本地通知+飞书推送 → 前端弹窗提醒
// 设置项: AutoPickEnable / AutoPickOvernightMode(一夜固定) / AutoPickNiuZhiMode(严选/标准/全勤)
// 注意: 一夜引擎14:50后用实时价(引擎内部处理); 牛指为日线口径, 14:50跑的是"当日尾盘快照"
// ==================================================================

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/duke-git/lancet/v2/strutil"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"go-stock/backend/data"
	"go-stock/backend/db"
	"go-stock/backend/logger"
	"go-stock/backend/models"
)

// EmitAutoPickDone 推送自动选股结果到前端（ctx为nil时跳过，测试环境无窗口）
func EmitAutoPickDone(a *App, day, msg string) {
	if a == nil || a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "autoPickDone", map[string]any{"day": day, "message": msg})
}

// autoPickRunning 防止cron与手动触发并发
var autoPickRunning atomic.Bool

// GetAutoPickSettings 读取自动选股设置（缺省: 关/标准模式）
func GetAutoPickSettings() (enabled bool, nzMode string) {
	nzMode = "标准"
	var v struct {
		AutoPickEnable     int
		AutoPickNiuZhiMode string
	}
	db.Dao.Raw(`SELECT auto_pick_enable as auto_pick_enable,
		COALESCE(auto_pick_niuzhi_mode,'标准') as auto_pick_niuzhi_mode
		FROM settings LIMIT 1`).Scan(&v)
	enabled = v.AutoPickEnable == 1
	if strutil.IsNotBlank(v.AutoPickNiuZhiMode) {
		nzMode = v.AutoPickNiuZhiMode
	}
	return
}

// SetAutoPickSettings 保存自动选股设置
func (a *App) SetAutoPickSettings(enabled bool, niuzhiMode string) string {
	defer PanicHandler()
	if niuzhiMode != "严选" && niuzhiMode != "标准" && niuzhiMode != "全勤" {
		niuzhiMode = "标准"
	}
	en := 0
	if enabled {
		en = 1
	}
	err := db.Dao.Model(&data.Settings{}).Where("id=?", data.NewSettingsApi().Config.ID).Updates(map[string]interface{}{
		"auto_pick_enable":      en,
		"auto_pick_niuzhi_mode": niuzhiMode,
	}).Error
	if err != nil {
		return "保存失败: " + err.Error()
	}
	state := "关"
	if enabled {
		state = "开"
	}
	return "已保存: 自动选股" + state + "，牛指模式" + niuzhiMode + "（每个工作日14:50自动执行）"
}

// GetAutoPickSettingsJSON 前端读取设置
func (a *App) GetAutoPickSettingsJSON() map[string]interface{} {
	defer PanicHandler()
	en, nz := GetAutoPickSettings()
	return map[string]interface{}{"enabled": en, "niuzhiMode": nz}
}

// RunAutoPickNow 手动触发一次自动选股（不判断时间，仅交易日判断交给引擎）
func (a *App) RunAutoPickNow() string {
	defer PanicHandler()
	return a.autoPickJob(true)
}

// autoPickCronJob cron入口: 只在工作日14:50触发（cron表达式已保证周一~五14:50，法定节假日由引擎内部日历判断）
func (a *App) autoPickCronJob() {
	a.autoPickJob(false)
}

// autoPickJob 执行主体: 跑两引擎 → 组装消息 → 推送（桌面通知+飞书）→ 前端事件
func (a *App) autoPickJob(manual bool) string {
	defer PanicHandler()
	if !autoPickRunning.CompareAndSwap(false, true) {
		return "自动选股正在进行中"
	}
	defer autoPickRunning.Store(false)

	today := time.Now().Format("2006-01-02")
	_, nzMode := GetAutoPickSettings()

	// 两引擎各跑各的（一夜无模式概念；牛指按设置模式）
	orn := (&App{}).RunOvernightScanJSON(today)
	nzn := (&App{}).RunNiuZhiScanJSON(today, nzMode)

	var sb strings.Builder
	sb.WriteString("【14:50自动选股 " + today + "】\n")

	// 一夜
	if !orn.Trading {
		sb.WriteString("\n◆ 一夜持股: " + orn.Note + "\n")
	} else {
		sb.WriteString(fmt.Sprintf("\n◆ 一夜持股 (%d只候选) | %s\n", len(orn.Cands), orn.Note))
		if len(orn.Cands) == 0 {
			sb.WriteString("  今日无信号 → 空仓\n")
		} else {
			top := orn.Cands[0]
			sb.WriteString(fmt.Sprintf("  Top1: %s %s 现价%.2f %+0.2f%% 建议仓%.0f%%\n", top.Code, top.Name, top.Price, top.Net, top.Weight*100))
			for i, c := range orn.Cands[1:] {
				if i >= 4 {
					break
				}
				sb.WriteString(fmt.Sprintf("  候补: %s %s 现价%.2f %+0.2f%% 仓%.0f%%\n", c.Code, c.Name, c.Price, c.Net, c.Weight*100))
			}
		}
	}

	// 牛指
	if !nzn.Trading {
		sb.WriteString("\n◆ 牛指T+1: " + nzn.Note + "\n")
	} else {
		sb.WriteString(fmt.Sprintf("\n◆ 牛指T+1[%s] (%d只候选) | %s\n", nzn.Mode, len(nzn.Cands), nzn.Note))
		if len(nzn.Cands) == 0 {
			sb.WriteString("  今日无信号 → 空仓\n")
		} else {
			top := nzn.Cands[0]
			sb.WriteString(fmt.Sprintf("  Top1: %s %s 现价%.2f %+0.2f%% 评分%d 建议仓%d%% %s\n",
				top.Code, top.Name, top.Price, top.Chg, top.Score, top.PosFinal, top.TopTag))
			for i, c := range nzn.Cands[1:] {
				if i >= 4 {
					break
				}
				sb.WriteString(fmt.Sprintf("  候补: %s %s 现价%.2f %+0.2f%% 评分%d 仓%d%%\n", c.Code, c.Name, c.Price, c.Chg, c.Score, c.PosFinal))
			}
		}
	}

	msg := sb.String()
	logger.SugaredLogger.Infof("自动选股完成:\n%s", msg)

	// v9e.9.6：自动写入策略跟踪批次（一夜/牛指各一签；当日已有批次则追加去重）
	autoPickSaveTrack(today, orn, nzn)

	// 桌面通知
	(&App{}).SendDesktopNotification("go-stock 自动选股", strings.ReplaceAll(msg, "\n", " "))

	// 飞书推送（开了飞书推送才发）
	cfg := data.NewSettingsApi().Config
	if cfg.FeishuPushEnable && strutil.IsNotBlank(cfg.FeishuRobot) {
		go data.NewFeishuAPI().SendFeishuMessage(msg)
	}

	// 前端事件（TrackCenter/任意页弹窗展示）
	EmitAutoPickDone(a, today, msg)
	return msg
}

// autoPickSaveTrack v9e.9.6 把自动选股结果写入策略跟踪批次（复用 CreateTrackBatch 的解析与去重逻辑）
// 一夜 → 策略"一夜持股"；牛指 → 策略"牛指T+1"。v9e.9.6.1修复CI编译错：writeOne收[]string、补models import。
func autoPickSaveTrack(day string, orn OvernightScanResult, nzn NiuZhiScanResult) {
	defer PanicHandler()
	writeOne := func(strategyName string, lines []string) {
		if len(lines) == 0 {
			return
		}
		var st models.TrackStrategy
		if err := db.Dao.Where("name = ?", strategyName).First(&st).Error; err != nil {
			logger.SugaredLogger.Errorf("自动记录失败: 找不到策略 %s", strategyName)
			return
		}
		a := &App{}
		msg := a.CreateTrackBatch(st.ID, day, strings.Join(lines, "\n"))
		logger.SugaredLogger.Infof("自动写入策略跟踪[%s]: %s", strategyName, msg)
	}
	// 一夜：Top1+候补（最多5只）
	var on []string
	for i, c := range orn.Cands {
		if i >= 5 {
			break
		}
		on = append(on, fmt.Sprintf("%s %s 仓%.0f%%", c.Code, c.Name, c.Weight*100))
	}
	writeOne("一夜持股", on)
	// 牛指：Top1+候补（最多5只）
	var nz []string
	for i, c := range nzn.Cands {
		if i >= 5 {
			break
		}
		nz = append(nz, fmt.Sprintf("%s %s 评分%d", c.Code, c.Name, c.Score))
	}
	writeOne("牛指T+1", nz)
}
