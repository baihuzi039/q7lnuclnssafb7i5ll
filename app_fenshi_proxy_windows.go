//go:build windows

package main

import (
	"net/url"
	"strings"
	"sync"

	"golang.org/x/sys/windows/registry"

	"go-stock/backend/logger"
)

// fenshiWindowsProxyAddr 读取 Windows 系统代理（Clash Verge「系统代理」开关写入注册表）
// 返回 "127.0.0.1:7897" 这类地址；未开启返回空串
func fenshiWindowsProxyAddr() string {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	en, _, _ := k.GetIntegerValue("ProxyEnable")
	if en == 0 {
		return ""
	}
	addr, _, _ := k.GetStringValue("ProxyServer")
	return addr
}

var fenshiSysProxyOnce sync.Once
var fenshiSysProxyURL string

// fenshiSysProxy 返回系统代理URL（非windows恒nil）
func fenshiSysProxy() *url.URL {
	fenshiSysProxyOnce.Do(func() {
		addr := fenshiWindowsProxyAddr()
		if addr == "" {
			return
		}
		if !strings.Contains(addr, "://") {
			addr = "http://" + addr
		}
		if _, err := url.Parse(addr); err == nil {
			fenshiSysProxyURL = addr
			logger.SugaredLogger.Infof("fenshi 使用系统代理: %s", addr)
		}
	})
	if fenshiSysProxyURL == "" {
		return nil
	}
	u, _ := url.Parse(fenshiSysProxyURL)
	return u
}

// fenshiSysProxyOrDirect 惰性取系统代理
func fenshiSysProxyOrDirect() *url.URL {
	return fenshiSysProxy()
}
