//go:build !windows

package main

import "syscall"

// fenshiHideWindow 非 Windows 平台返回 nil（无控制台窗口概念）
func fenshiHideWindow() *syscall.SysProcAttr { return nil }
