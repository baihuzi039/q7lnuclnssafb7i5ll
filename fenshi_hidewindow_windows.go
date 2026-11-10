package main

import "syscall"

// fenshiHideWindow Windows专用：隐藏 PowerShell 子进程控制台窗口（CREATE_NO_WINDOW）
func fenshiHideWindow() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
