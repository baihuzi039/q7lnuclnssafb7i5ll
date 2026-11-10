//go:build !windows

package main

import "net/url"

// fenshiSysProxy 非windows平台无系统代理读取，恒nil（直连）
func fenshiSysProxy() *url.URL { return nil }

// fenshiSysProxyOrDirect 非windows恒nil
func fenshiSysProxyOrDirect() *url.URL { return nil }
