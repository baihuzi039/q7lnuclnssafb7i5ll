package main

// ==================================================================
// [v9e] 14:50 自动选股（两引擎制度闸门已过：一夜453只100%重合 / 牛指长窗口8基准日一致+2日判责基准数据缺）
// [v9f] 升级为收盘后自动任务（时间可调，默认17:40）：同步日线→选股→建批次→自动追加回测表→通知
// 设置项: AutoPickEnable / AutoPickTime(HH:MM) / AutoPickNiuZhiMode(严选/标准/全勤) / AutoAppendXlsx
// 注: 保留手动"立即执行"入口；17:40后引擎用当日收盘数据（本地库已同步），不再是盘中快照
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
// [v9f] 追加返回值：运行时间 HH:MM（默认17:40）与自动追加回测表开关（默认开）
func GetAutoPickSettings() (enabled bool, nzMode string) {
	enabled, nzMode, _, _ = GetAutoPickSettingsFull()
	return
}

// GetAutoPickSettingsFull [v9f] 完整读取: 开关/牛指模式/运行时间/自动追加回测表
func GetAutoPickSettingsFull() (enabled bool, nzMode, runAt string, appendXlsx bool) {
	nzMode = "标准"
	runAt = "17:40"
	appendXlsx = true
	var v struct {
		AutoPickEnable     int
		AutoPickNiuZhiMode string
		AutoPickTime       string
		AutoAppendXlsx     int
	}
	db.Dao.Raw(`SELECT auto_pick_enable as auto_pick_enable,
		COALESCE(auto_pick_niuzhi_mode,'标准') as auto_pick_niuzhi_mode,
		COALESCE(auto_pick_time,'17:40') as auto_pick_time,
		COALESCE(auto_append_xlsx,1) as auto_append_xlsx
		FROM settings LIMIT 1`).Scan(&v)
	enabled = v.AutoPickEnable == 1
	if strutil.IsNotBlank(v.AutoPickNiuZhiMode) {
		nzMode = v.AutoPickNiuZhiMode
	}
	if strutil.IsNotBlank(v.AutoPickTime) {
		runAt = v.AutoPickTime
	}
	appendXlsx = v.AutoAppendXlsx != 0
	return
}

// SetAutoPickSettings 保存自动选股设置
// SetAutoPickSettings [v9f] 保存设置（新增 runAt HH:MM 与 appendXlsx 开关）
func (a *App) SetAutoPickSettings(enabled bool, niuzhiMode string) string {
	defer PanicHandler()
	_, _, oldAt, oldAp := GetAutoPickSettingsFull()
	return a.SetAutoPickSettingsFull(enabled, niuzhiMode, oldAt, oldAp)
}

// SetAutoPickSettingsFull [v9f] 完整保存（runAt格式 HH:MM，非法回退17:40）
func (a *App) SetAutoPickSettingsFull(enabled bool, niuzhiMode, runAt string, appendXlsx bool) string {
	defer PanicHandler()
	if niuzhiMode != "严选" && niuzhiMode != "标准" && niuzhiMode != "全勤" {
		niuzhiMode = "标准"
	}
	// 时间校验：HH:MM 24小时制
	runAt = strings.TrimSpace(runAt)
	if t, err := time.Parse("15:04", runAt); err != nil {
		runAt = "17:40"
	} else {
		runAt = t.Format("15:04")
	}
	en, ap := 0, 0
	if enabled {
		en = 1
	}
	if appendXlsx {
		ap = 1
	}
	err := db.Dao.Model(&data.Settings{}).Where("id=?", data.NewSettingsApi().Config.ID).Updates(map[string]interface{}{
		"auto_pick_enable":      en,
		"auto_pick_niuzhi_mode": niuzhiMode,
		"auto_pick_time":        runAt,
		"auto_append_xlsx":      ap,
	}).Error
	if err != nil {
		return "保存失败: " + err.Error()
	}
	state := "关"
	if enabled {
		state = "开"
	}
	return "已保存: 收盘后自动任务" + state + "，运行时间 " + runAt + "，牛指模式" + niuzhiMode + "，自动追加回测表" + map[bool]string{true: "开", false: "关"}[appendXlsx]
}

// GetAutoPickSettingsJSON 前端读取设置（[v9f] 含时间与追加开关）
func (a *App) GetAutoPickSettingsJSON() map[string]interface{} {
	defer PanicHandler()
	en, nz, runAt, ap := GetAutoPickSettingsFull()
	return map[string]interface{}{"enabled": en, "niuzhiMode": nz, "runAt": runAt, "appendXlsx": ap}
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
	_, nzMode, runAt, appendXlsx := GetAutoPickSettingsFull()

	// [v9f] 收盘后跑：先全量同步日线到最新（含今日收盘），保证选股/建批次/回填/追表全用最新数据。
	// 仅 cron 触发时同步（手动触发时用户一般刚同步过，避免重复等待）；同步是异步等待完成。
	if !manual {
		syncDailyKLineJob(nil)
	}

	// 两引擎各跑各的（一夜无模式概念；牛指按设置模式）
	orn := (&App{}).RunOvernightScanJSON(today)
	nzn := (&App{}).RunNiuZhiScanJSON(today, nzMode)

	var sb strings.Builder
	sb.WriteString("【收盘后自动任务 " + today + " " + runAt + "】\n")

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

	// [v9f] 自动追加内置回测表（data/templates/<策略名>.xlsx），开关控制
	if appendXlsx {
		if xmsg := AutoAppendTrackXlsx(today); xmsg != "" {
			sb.WriteString("\n〖回测表〗\n" + xmsg + "\n")
		}
	}

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
