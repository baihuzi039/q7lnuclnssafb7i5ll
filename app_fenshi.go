package main

// ==================================================================
// 分时辅助引擎（FenshiCenter 后端）
// 经验来源: workspace/fsa 2022-2025 全市场1分钟数据验证结论
//   1) 承接战法: 高开1-6% + 量比>=3 + 盘中急跌>=3% -> 样本外冲高2%胜率47.7%, 期望+1.06%/笔 (核心信号)
//   2) 量价齐升抢筹: 历史胜率仅约7% (风险警示)
//   3) 追高形态: 涨幅>7%追入 胜率约18%, 隔夜均值约-2% (风险警示)
//   4) 高开5-7%组: 次日开盘均值约-9.9%, 4年一致 (隔夜风险警示)
//   5) 均价线(VWAP)位置/15分钟动量/量能突破 作为盘中强弱参考
// 数据源: 东方财富 push2 clist(全市场扫描) + trends2(当日分时) + qt/stock/get(快照)
// ==================================================================

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"

	"go-stock/backend/logger"
)

// FenshiTick 分时数据点
type FenshiTick struct {
	Time string  `json:"time"`
	Open float64 `json:"open"`
	Now  float64 `json:"now"`
	High float64 `json:"high"`
	Low  float64 `json:"low"`
	Vol  float64 `json:"vol"`
	Amt  float64 `json:"amt"`
	Avg  float64 `json:"avg"`
}

// FenshiCand 候选池条目
type FenshiCand struct {
	Code       string  `json:"code"`
	Name       string  `json:"name"`
	Price      float64 `json:"price"`      // 现价
	ChangePct  float64 `json:"changePct"`  // 涨跌幅%
	VolumeRate float64 `json:"volumeRate"` // 量比
	Turnover   float64 `json:"turnover"`   // 换手率%
	Amount     float64 `json:"amount"`     // 成交额(元)
	Board      string  `json:"board"`      // 板块归属(简要)
}

// FenshiSignal 单条信号/建议
type FenshiSignal struct {
	Name string `json:"name"`
	Type string `json:"type"` // buy / warn / info
	Prob string `json:"prob"` // 概率/依据描述
	Desc string `json:"desc"`
}

// FenshiAnalysisResult 个股分时分析结果
type FenshiAnalysisResult struct {
	Code        string       `json:"code"`
	Name        string       `json:"name"`
	Price       float64      `json:"price"`
	PrevClose   float64      `json:"prevClose"`
	GapPct      float64      `json:"gapPct"` // 高开%
	ChangePct   float64      `json:"changePct"`
	AvgPrice    float64      `json:"avgPrice"`   // 均价线
	AboveVwapP  float64      `json:"aboveVwapP"` // 现价偏离均价线%
	Mom15       float64      `json:"mom15"`      // 15分钟动量%
	VolumeRate  float64      `json:"volumeRate"`
	PullbackPct float64      `json:"pullbackPct"` // 距开盘后最高点回落%
	High        float64      `json:"high"`
	Low         float64      `json:"low"`
	Trend       []FenshiTick `json:"trend"` // 分时数据点

	Signals []FenshiSignal `json:"signals"`
	Advice  string         `json:"advice"`
	ErrMsg  string         `json:"errMsg"` // 非空表示分析失败及原因
}

// fenshiDialTLS 用 utls 模拟 Chrome TLS 指纹，防东财风控识别 Go 默认 crypto/tls 导致 EOF 断连
func fenshiDialTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var rawConn net.Conn
	// 有系统代理时先建 CONNECT 隧道（否则 utls 直接对目标裸连，绕过了代理）
	if pu := fenshiSysProxy(); pu != nil {
		pconn, derr := net.DialTimeout("tcp", pu.Host, 3*time.Second)
		if derr != nil {
			return nil, fmt.Errorf("连接系统代理失败: %v", derr)
		}
		req := &http.Request{
			Method: "CONNECT",
			URL:    &url.URL{Opaque: addr},
			Host:   addr,
			Header: http.Header{},
		}
		if err := req.Write(pconn); err != nil {
			pconn.Close()
			return nil, err
		}
		br := bufio.NewReader(pconn)
		resp, err := http.ReadResponse(br, req)
		if err != nil {
			pconn.Close()
			return nil, fmt.Errorf("代理CONNECT失败: %v", err)
		}
		if resp.StatusCode != 200 {
			pconn.Close()
			return nil, fmt.Errorf("代理CONNECT状态: %s", resp.Status)
		}
		rawConn = pconn
	} else {
		rc, derr := net.DialTimeout(network, addr, 3*time.Second)
		if derr != nil {
			return nil, derr
		}
		rawConn = rc
	}
	cfg := &utls.Config{ServerName: host, NextProtos: []string{"http/1.1"}}
	uconn := utls.UClient(rawConn, cfg, utls.HelloChrome_Auto)
	if err := uconn.HandshakeContext(ctx); err != nil {
		rawConn.Close()
		return nil, err
	}
	return uconn, nil
}

// fenshiAnalyzeFromTencent 腾讯行情兑底：分时分钟线 + 实时快照(昨收/今开)，无风控限制
func fenshiAnalyzeFromTencent(stockCode string) (*FenshiAnalysisResult, error) {
	tcode, ok := fenshiTencentCode(stockCode)
	if !ok {
		return nil, fmt.Errorf("无法转换腾讯代码: %s", stockCode)
	}
	// 1) 分钟线
	minURL := fmt.Sprintf("https://web.ifzq.gtimg.cn/appstock/app/minute/query?code=%s", tcode)
	m, err := fenshiGetOnce(minURL, fenshiPlainClient)
	if err != nil {
		return nil, fmt.Errorf("腾讯分钟线请求失败（请检查代理/加速器/防火墙）: %v", err)
	}
	data, _ := m["data"].(map[string]any)
	stk, _ := data[tcode].(map[string]any)
	dd, _ := stk["data"].(map[string]any)
	rows, _ := dd["data"].([]any)
	if len(rows) == 0 {
		// 返回了 HTML 而非 JSON → 用户网络把腾讯也劫持/代理拦截了
		return nil, fmt.Errorf("腾讯返回非数据内容（你的网络可能开了代理/加速器/安全软件拦截了行情请求，请关闭后重试）")
	}
	var trend []FenshiTick

	firstPx := 0.0
	for _, r := range rows {
		line, _ := r.(string)
		fs := strings.Fields(line)
		if len(fs) < 4 {
			continue
		}
		px, _ := strconv.ParseFloat(fs[1], 64)
		cv, _ := strconv.ParseFloat(fs[2], 64) // 累计量(手)
		ca, _ := strconv.ParseFloat(fs[3], 64) // 累计额(元)
		t := fs[0]
		if len(t) == 4 {
			t = t[:2] + ":" + t[2:]
		}
		pv := cv * 100
		avg := 0.0
		if pv > 0 {
			avg = ca / pv
		}
		if firstPx == 0 {
			firstPx = px
		}
		// 腾讯无单根高低价，用现价代替
		trend = append(trend, FenshiTick{Time: t, Open: px, Now: px, High: px, Low: px, Vol: pv, Amt: ca, Avg: avg})
	}
	if len(trend) == 0 {
		return nil, fmt.Errorf("腾讯分钟线解析为空")
	}
	// 2) 快照（昨收）—— qt.gtimg.cn 返回 v_sh600519="1~名称~代码~现价~昨收~..." 纯文本，不能用JSON解析（v9f.15修复）
	snapURL := fmt.Sprintf("https://qt.gtimg.cn/q=%s", tcode)
	sreq, _ := http.NewRequest("GET", snapURL, nil)
	sreq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	sresp, err := fenshiPlainClient.Do(sreq)
	if err != nil {
		return nil, fmt.Errorf("腾讯快照请求失败: %v", err)
	}
	sbody, _ := io.ReadAll(sresp.Body)
	sresp.Body.Close()
	preClose, name := 0.0, ""
	if i := strings.Index(string(sbody), "\""); i >= 0 {
		body := string(sbody)[i+1:]
		if j := strings.LastIndex(body, "\""); j >= 0 {
			body = body[:j]
		}
		parts := strings.Split(body, "~")
		if len(parts) > 4 {
			name = parts[1]
			preClose, _ = strconv.ParseFloat(parts[4], 64)
		}
	}
	if preClose <= 0 {
		return nil, fmt.Errorf("腾讯快照昨收异常")
	}
	// 3) 复用现有指标计算：临时构造与东财相同的结果
	last := trend[len(trend)-1]
	openPx := trend[0].Now
	nowPx := last.Now
	highPx, lowPx := nowPx, nowPx
	for _, tk := range trend {
		if tk.High > highPx {
			highPx = tk.High
		}
		if tk.Low < lowPx {
			lowPx = tk.Low
		}
	}
	vwap := last.Avg
	openPct := 0.0
	if preClose > 0 {
		openPct = (openPx - preClose) / preClose * 100
	}
	vwapDev := 0.0
	if vwap > 0 {
		vwapDev = (nowPx - vwap) / vwap * 100
	}
	n := len(trend)
	mom15 := 0.0
	if n > 15 && trend[n-16].Now > 0 {
		mom15 = (nowPx - trend[n-16].Now) / trend[n-16].Now * 100
	}
	fallFromHigh := 0.0
	if highPx > 0 {
		fallFromHigh = (nowPx - highPx) / highPx * 100
	}
	res := &FenshiAnalysisResult{
		Code: stockCode, Name: name,
		Price: nowPx, PrevClose: preClose,
		GapPct: openPct, ChangePct: (nowPx - preClose) / preClose * 100,
		AvgPrice: vwap, AboveVwapP: vwapDev,
		Mom15: mom15, VolumeRate: 0, // 腾讯无量比基准，置0不误报
		PullbackPct: fallFromHigh, High: highPx, Low: lowPx,
		Trend: trend,
	}
	res.Signals = fenshiBuildSignals(res)
	// 兑底说明
	res.Advice = fmt.Sprintf("[腾讯源] %s 昨收%.2f 今开%.2f 现价%.2f（量比不可用，其余指标正常）", name, preClose, openPx, nowPx)
	return res, nil
}

// fenshiTencentCode 证券代码转腾讯格式：600519→sh600519, 000001→sz000001, 300001→sz300001, 830001→bj830001
func fenshiTencentCode(code string) (string, bool) {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return "", false
	}
	switch {
	case strings.HasPrefix(code, "6"):
		return "sh" + code, true
	case strings.HasPrefix(code, "8") || strings.HasPrefix(code, "4") || strings.HasPrefix(code, "9"):
		return "bj" + code, true
	default:
		return "sz" + code, true
	}
}

// fenshiDirectClient 与原作者 SharedHTTPClient 同款：直连 + Go 默认 crypto/tls，不走代理不装指纹。
// 实测证明候选池扫描（同款路径）在此网络环境正常，故设为第一优先。
// fenshiUrls 返回域名备援列表：主域 + 92备用节点（东财同一接口多域名部署）
func fenshiUrls(rawURL string) []string {
	urls := []string{rawURL}
	if strings.Contains(rawURL, "push2his.eastmoney.com") {
		urls = append(urls, strings.Replace(rawURL, "push2his.eastmoney.com", "92.push2his.eastmoney.com", 1))
	} else if strings.Contains(rawURL, "push2.eastmoney.com") {
		urls = append(urls, strings.Replace(rawURL, "push2.eastmoney.com", "92.push2.eastmoney.com", 1))
	}
	return urls
}

var fenshiDirectClient = &http.Client{
	Timeout: 3 * time.Second,
	Transport: &http.Transport{
		Proxy:                 nil,
		TLSHandshakeTimeout:   3 * time.Second,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       60 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
	},
}

var fenshiHTTPClient = &http.Client{
	Timeout: 3 * time.Second,
	Transport: &http.Transport{
		Proxy:               http.ProxyURL(fenshiSysProxyOrDirect()),
		DialTLSContext:      fenshiDialTLS,
		TLSHandshakeTimeout: 3 * time.Second,
		ForceAttemptHTTP2:   false,
		MaxIdleConns:        10,
		IdleConnTimeout:     60 * time.Second,
	},
}

var fenshiUAList = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Edg/126.0.0.0",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:128.0) Gecko/20100101 Firefox/128.0",
}

// fenshiGet 拉取并解析JSON（多重备援：http明文优先→https+utls→备用域名，防东财风控断连EOF）
// 东财接口支持 http 明文访问，且风控只针对 TLS 握手层；明文请求不被拦截，优先使用。
var fenshiPlainClient = &http.Client{
	Timeout: 3 * time.Second,
	Transport: &http.Transport{
		Proxy: http.ProxyURL(fenshiSysProxyOrDirect()),
	},
}

// fenshiPSFetch 用 Windows 自带 PowerShell(schannel TLS 栈) 拉取 JSON。
// 适用场景：链路对 Go crypto/tls 指纹及明文 http 做 TLS 识别阻断(EOF/502/劫持HTML)时，
// schannel 指纹与 Chrome 同源，浏览器能通它就能通。仅作最末级兑底。
func fenshiPSFetch(rawURL string) (map[string]any, error) {
	if _, err := exec.LookPath("powershell"); err != nil {
		return nil, fmt.Errorf("powershell不可用")
	}
	script := fmt.Sprintf(
		`$r=Invoke-WebRequest -Uri '%s' -UseBasicParsing -TimeoutSec 8 -Headers @{'User-Agent'='Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36'}; $r.Content`,
		strings.ReplaceAll(rawURL, "'", "''"))
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	// v9f.16：隐藏控制台窗口，避免分析时弹出蓝/黑框影响体验
	cmd.SysProcAttr = fenshiHideWindow()
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	body := strings.TrimSpace(string(out))
	// PowerShell 可能输出 BOM/裹挙的转义，去头找 JSON 起点
	if i := strings.Index(body, "{"); i > 0 {
		body = body[i:]
	}
	var m map[string]any
	err = json.Unmarshal([]byte(body), &m)
	return m, err
}

func fenshiGet(rawURL string) (map[string]any, error) {
	var lastErr error
	// 第0优先：直连 + Go 默认 TLS（与候选池扫描/原作者同款路径，实测最稳）
	for i, u := range fenshiUrls(rawURL) {
		m, err := fenshiGetOnce(u, fenshiDirectClient)
		if err == nil {
			return m, nil
		}
		lastErr = err
		logger.SugaredLogger.Warnf("fenshiGet 直连第%d次失败: %v", i+1, err)
	}
	// 第1优先：http 明文（无TLS指纹，风控不拦）
	if strings.HasPrefix(rawURL, "https://") {
		plainURL := "http://" + rawURL[len("https://"):]
		m, err := fenshiGetOnce(plainURL, fenshiPlainClient)
		if err == nil {
			return m, nil
		}
		lastErr = err
		logger.SugaredLogger.Warnf("fenshiGet http明文失败，转https: %v", err)
	}
	for i, u := range fenshiUrls(rawURL) {
		m, err := fenshiGetOnce(u, fenshiHTTPClient)
		if err == nil {
			return m, nil
		}
		lastErr = err
		logger.SugaredLogger.Warnf("fenshiGet 第%d次失败: %v", i+1, err)
	}
	// 最末级兑底：PowerShell schannel 通道（链路对 Go 流量全面阻断时的唯一生路）
	if m, err := fenshiPSFetch(rawURL); err == nil {
		logger.SugaredLogger.Info("fenshiGet PowerShell 兑底成功")
		return m, nil
	} else {
		logger.SugaredLogger.Warnf("fenshiGet PowerShell 兑底失败: %v", err)
	}
	return nil, lastErr
}

func fenshiGetOnce(url string, client *http.Client) (map[string]any, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	// 随机 UA + 完整浏览器头，降低被风控识别概率
	ua := fenshiUAList[time.Now().UnixNano()%int64(len(fenshiUAList))]
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Referer", "https://quote.eastmoney.com/")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	err = json.Unmarshal(body, &m)
	return m, err
}

// fenshiToFloat 安全转float("-"等返回0)
func fenshiToFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f
	case int64:
		return float64(t)
	}
	return 0
}

// fenshiSecid 代码转东财 secid
func fenshiSecid(code string) string {
	code = strings.TrimSpace(code)
	if strings.Contains(code, ".") {
		return code
	}
	if strings.HasPrefix(code, "6") || strings.HasPrefix(code, "9") {
		return "1." + code
	}
	if strings.HasPrefix(code, "8") || strings.HasPrefix(code, "4") || strings.HasPrefix(code, "92") {
		return "0." + code
	}
	return "0." + code
}

// FenshiScanCandidates 全市场扫描候选池: 高开1-7% + 量比>=3, 剔除ST/退市
func (a *App) FenshiScanCandidates() []FenshiCand {
	markets := []string{
		"m:0+t:6,m:0+t:80,m:1+t:2,m:1+t:23", // 沪深主板+创业板
		"m:0+t:81+s:2048",                   // 科创板
	}
	var out []FenshiCand
	seen := map[string]bool{}
	for _, fs := range markets {
		for pn := 1; pn <= 5; pn++ {
			url := fmt.Sprintf("https://push2.eastmoney.com/api/qt/clist/get?pn=%d&pz=100&po=1&np=1&fltt=2&invt=2&fid=f3&fs=%s&fields=f2,f3,f8,f10,f12,f14",
				pn, fs)
			m, err := fenshiGet(url)
			if err != nil {
				logger.SugaredLogger.Errorf("FenshiScanCandidates req error: %v", err)
				break
			}
			d, _ := m["data"].(map[string]any)
			if d == nil {
				break
			}
			diff, _ := d["diff"].([]any)
			if len(diff) == 0 {
				break
			}
			for _, item := range diff {
				row, ok := item.(map[string]any)
				if !ok {
					continue
				}
				code := fmt.Sprintf("%v", row["f12"])
				name := fmt.Sprintf("%v", row["f14"])
				if seen[code] {
					continue
				}
				chg := fenshiToFloat(row["f3"])
				vr := fenshiToFloat(row["f10"])
				if chg < 1.0 || chg > 7.0 || vr < 3.0 {
					continue
				}
				if strings.Contains(name, "ST") || strings.Contains(name, "退") {
					continue
				}
				seen[code] = true
				out = append(out, FenshiCand{
					Code:       code,
					Name:       name,
					Price:      fenshiToFloat(row["f2"]),
					ChangePct:  chg,
					VolumeRate: vr,
					Turnover:   fenshiToFloat(row["f8"]),
					Board:      "",
				})
			}
			total, _ := d["total"].(float64)
			if float64(pn*100) >= total {
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].VolumeRate > out[j].VolumeRate })
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}

// FenshiAnalyzeStock 个股实时分时分析（核心入口）
// FenshiAnalyzeStock 入口：VPN分流不稳定导致"时好时坏"，外层重试2轮大幅提高成功率
var (
	fenshiJobMu      sync.Mutex
	fenshiJobRunning = map[string]bool{}
)

// fenshiPSFetchMemoPS 去重保护，防止同一代码并发重复分析拖死前端
var fenshiJobInflight = map[string]*FenshiAnalysisResult{}

func (a *App) FenshiAnalyzeStock(stockCode string) *FenshiAnalysisResult {
	fenshiJobMu.Lock()
	if res, ok := fenshiJobInflight[stockCode]; ok {
		fenshiJobMu.Unlock()
		return res
	}
	fenshiJobMu.Unlock()

	fenshiJobMu.Lock()
	fenshiJobInflight[stockCode] = nil // 占位
	fenshiJobMu.Unlock()

	var r *FenshiAnalysisResult
	for round := 1; round <= 2; round++ {
		r = fenshiAnalyzeCore(stockCode)
		if r != nil && r.ErrMsg == "" {
			break
		}
		logger.SugaredLogger.Warnf("FenshiAnalyzeStock 第%d轮失败，重试", round)
	}

	fenshiJobMu.Lock()
	fenshiJobInflight[stockCode] = r
	fenshiJobMu.Unlock()
	return r
}

// fenshiAnalyzeCore 单轮分析：东财→腾讯→新浪三级兜底
func fenshiAnalyzeCore(stockCode string) *FenshiAnalysisResult {
	secid := fenshiSecid(stockCode)
	url := fmt.Sprintf("https://push2his.eastmoney.com/api/qt/stock/trends2/get?secid=%s&fields1=f1,f2,f3,f4,f5,f6,f7,f8,f9,f10,f11,f12,f13&fields2=f51,f52,f53,f54,f55,f56,f57,f58&iscr=0&ndays=1", secid)
	retErr := func(msg string) *FenshiAnalysisResult {
		logger.SugaredLogger.Errorf("FenshiAnalyzeStock %s: %s", stockCode, msg)
		return &FenshiAnalysisResult{Code: stockCode, ErrMsg: msg}
	}
	// v9f.16：东财直连在本机被阻断已成定局，直接以腾讯为主源（实测分钟线+快照全通），
	// 省掉东财多级重试的漫长等待（这是"分析卡死"的主因）
	res, terr := fenshiAnalyzeFromTencent(stockCode)
	if terr == nil {
		return res
	}
	logger.SugaredLogger.Warnf("FenshiAnalyzeStock 腾讯失败，转新浪源: %v", terr)
	res2, serr := fenshiAnalyzeFromSina(stockCode)
	if serr == nil {
		return res2
	}
	// 腾讯、新浪都挂才走东财全链（含PowerShell兜底）
	m, err := fenshiGet(url)
	if err != nil {
		proxyInfo := "无(裸连)"
		if pu := fenshiSysProxy(); pu != nil {
			proxyInfo = pu.Host
		}
		return retErr("三源均失败。[系统代理=" + proxyInfo + " 版本=" + Version + "] 腾讯: " + terr.Error() + "；新浪: " + serr.Error() + "；东财: " + err.Error())
	}
	d, _ := m["data"].(map[string]any)
	if d == nil {
		return retErr("未获取到分时数据（可能代码不存在，或非交易时段无当日数据），代码: " + stockCode)
	}
	name, _ := d["name"].(string)
	preClose := fenshiToFloat(d["preClose"])
	trends, _ := d["trends"].([]any)
	if len(trends) == 0 || preClose <= 0 {
		return retErr("分时数据为空（接口返回无趋势数据），代码: " + stockCode)
	}

	resE := &FenshiAnalysisResult{Code: stockCode, Name: name, PrevClose: preClose, Trend: make([]FenshiTick, 0, len(trends))}
	var openPx, highPx, lowPx, nowPx, avgPx, px15 float64
	var idx15 int
	// 解析 trends: "09:30,open,now,high,low,vol,amount,avg"
	for i, raw := range trends {
		s, _ := raw.(string)
		parts := strings.Split(s, ",")
		if len(parts) < 8 {
			continue
		}
		op := fenshiToFloat(parts[1])
		np := fenshiToFloat(parts[2])
		hp := fenshiToFloat(parts[3])
		lp := fenshiToFloat(parts[4])
		ap := fenshiToFloat(parts[7])
		if i == 0 {
			openPx = op
		}
		if hp > highPx {
			highPx = hp
		}
		if lowPx <= 0 || lp < lowPx {
			lowPx = lp
		}
		nowPx = np
		avgPx = ap
		if i >= 15 {
			px15 = fenshiToFloat(strings.Split(fmt.Sprintf("%v", trends[i-15]), ",")[2])
			idx15 = i
		}
		_ = idx15
		resE.Trend = append(resE.Trend, FenshiTick{Time: parts[0], Open: op, Now: np, High: hp, Low: lp, Vol: fenshiToFloat(parts[5]), Amt: fenshiToFloat(parts[6]), Avg: ap})
	}
	if openPx <= 0 || nowPx <= 0 {
		return retErr("分时数据解析异常（价格均为0），代码: " + stockCode)
	}
	resE.Price = nowPx
	resE.GapPct = (openPx/preClose - 1) * 100
	resE.ChangePct = (nowPx/preClose - 1) * 100
	resE.High = highPx
	resE.Low = lowPx
	resE.AvgPrice = avgPx
	if avgPx > 0 {
		resE.AboveVwapP = (nowPx/avgPx - 1) * 100
	}
	if px15 > 0 {
		resE.Mom15 = (nowPx/px15 - 1) * 100
	}
	if highPx > 0 {
		resE.PullbackPct = (1 - nowPx/highPx) * 100
	}
	// 量比: 快照 f10
	snapURL := fmt.Sprintf("https://push2.eastmoney.com/api/qt/stock/get?secid=%s&fields=f10,f12,f14", secid)
	sm, err := fenshiGet(snapURL)
	if err == nil {
		if sd, _ := sm["data"].(map[string]any); sd != nil {
			resE.VolumeRate = fenshiToFloat(sd["f10"])
			if n, ok := sd["f14"].(string); ok {
				resE.Name = n
			}
		}
	}

	resE.Signals = fenshiBuildSignals(resE)
	resE.Advice = fenshiAdviceText(resE)
	return resE
}

// fenshiBuildSignals 按 fsa 验证结论生成信号
func fenshiBuildSignals(r *FenshiAnalysisResult) []FenshiSignal {
	var sigs []FenshiSignal
	gap, pullback, chg, vwap := r.GapPct, r.PullbackPct, r.ChangePct, r.AboveVwapP

	// 1. 承接战法（核心买点信号, 2022-2025 验证）
	if gap >= 1 && gap <= 6 && r.VolumeRate >= 3 && pullback >= 3 {
		sigs = append(sigs, FenshiSignal{
			Name: "承接战法·关注", Type: "buy", Prob: "样本外冲高2%胜率约47.7%，期望约+1.06%/笔（4年稳健）",
			Desc: fmt.Sprintf("高开%.1f%%+量比%.1f+盘中回落%.1f%%，符合承接战法形态，关注分时企稳后的承接力度", gap, r.VolumeRate, pullback),
		})
	}
	// 2. 抢筹警示（历史胜率仅约7%）
	if gap >= 1 && chg > gap+3 && r.VolumeRate >= 5 {
		sigs = append(sigs, FenshiSignal{
			Name: "量价齐升抢筹·警惕", Type: "warn", Prob: "历史同类形态胜率仅约7%",
			Desc: "竞价高开后持续放量拉升，属情绪抢筹形态，历史胜率极低，谨防冲高回落接力失败",
		})
	}
	// 3. 追高警示
	if chg >= 7 {
		sigs = append(sigs, FenshiSignal{
			Name: "追高风险警示", Type: "warn", Prob: "历史追高形态胜率约18%，隔夜均值约-2%",
			Desc: fmt.Sprintf("现价涨幅%.1f%%已进入历史统计高危区，追入胜率低且隔夜风险大", chg),
		})
	}
	// 4. 高开隔夜风险
	if gap >= 5 {
		sigs = append(sigs, FenshiSignal{
			Name: "大幅高开隔夜风险", Type: "warn", Prob: "高开5-7%组次日开盘均值约-9.9%（4年一致）",
			Desc: fmt.Sprintf("高开%.1f%%，历史大幅高开股次日开盘平均大幅低开，持仓过夜需极度谨慎", gap),
		})
	}
	// 5. 均价线强弱
	if vwap > 1.5 {
		sigs = append(sigs, FenshiSignal{Name: "均价线上方·强势", Type: "info", Prob: "盘中强弱参考", Desc: fmt.Sprintf("现价高于分时均价线%.1f%%，盘中承接较强", vwap)})
	} else if vwap < -1.5 {
		sigs = append(sigs, FenshiSignal{Name: "均价线下方·走弱", Type: "info", Prob: "盘中强弱参考", Desc: fmt.Sprintf("现价低于分时均价线%.1f%%，反弹偏弱，注意均价线压力", vwap)})
	}
	// 6. 15分钟动量
	if r.Mom15 >= 2 {
		sigs = append(sigs, FenshiSignal{Name: "短线动量向上", Type: "info", Prob: "盘中强弱参考", Desc: fmt.Sprintf("近15分钟上涨%.1f%%，分时动能向上", r.Mom15)})
	} else if r.Mom15 <= -2 {
		sigs = append(sigs, FenshiSignal{Name: "短线动量向下", Type: "info", Prob: "盘中强弱参考", Desc: fmt.Sprintf("近15分钟下跌%.1f%%，分时走弱", r.Mom15)})
	}
	return sigs
}

// fenshiAdviceText 汇总建议文本
func fenshiAdviceText(r *FenshiAnalysisResult) string {
	var sb strings.Builder
	hasBuy, hasWarn := false, false
	for _, s := range r.Signals {
		switch s.Type {
		case "buy":
			hasBuy = true
		case "warn":
			hasWarn = true
		}
	}
	if hasWarn {
		sb.WriteString("【风险提示】存在历史统计高危形态信号，")
	}
	if hasBuy {
		sb.WriteString("【关注】出现承接战法形态（历史样本外胜率约47.7%），")
	}
	if !hasBuy && !hasWarn {
		sb.WriteString("【中性】当前分时形态无显著历史优势信号，")
	}
	if r.AboveVwapP > 0 {
		sb.WriteString(fmt.Sprintf("现价在均价线上方(+%.1f%%)偏强；", r.AboveVwapP))
	} else {
		sb.WriteString(fmt.Sprintf("现价在均价线下方(%.1f%%)偏弱；", r.AboveVwapP))
	}
	sb.WriteString(fmt.Sprintf("高开%.1f%%、现涨%.1f%%、量比%.1f。以上为历史统计参考，非投资建议。", r.GapPct, r.ChangePct, r.VolumeRate))
	return sb.String()
}

// FenshiHotBoards 热门板块（含涨停家数）
func (a *App) FenshiHotBoards() []map[string]any {
	url := "https://push2.eastmoney.com/api/qt/clist/get?pn=1&pz=15&po=1&np=1&fltt=2&invt=2&fid=f3&fs=m:90+t:2&fields=f3,f12,f14,f104,f128"
	m, err := fenshiGet(url)
	if err != nil {
		logger.SugaredLogger.Errorf("FenshiHotBoards error: %v", err)
		return nil
	}
	d, _ := m["data"].(map[string]any)
	if d == nil {
		return nil
	}
	diff, _ := d["diff"].([]any)
	var out []map[string]any
	for _, item := range diff {
		if row, ok := item.(map[string]any); ok {
			out = append(out, row)
		}
	}
	return out
}

// fenshiAnalyzeFromSina 新浪行情兜底（第三级）：快照 hq.sinajs.cn + 5分钟K线合成
// 需带 Referer: finance.sina.com.cn；JSON 数组 [{day,open,high,low,close,volume}]
func fenshiAnalyzeFromSina(stockCode string) (*FenshiAnalysisResult, error) {
	tcode, ok := fenshiTencentCode(stockCode)
	if !ok {
		return nil, fmt.Errorf("无法转换代码: %s", stockCode)
	}
	// 1) 5分钟K线（datalen=48 覆盖全天）
	kURL := fmt.Sprintf("https://vip.stock.finance.sina.com.cn/quotes_service/api/json_v2.php/CN_MarketData.getKLineData?symbol=%s&scale=5&ma=no&datalen=48", tcode)
	req, err := http.NewRequest("GET", kURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Referer", "https://finance.sina.com.cn/")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/131.0.0.0 Safari/537.36")
	resp, err := fenshiPlainClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("新浪K线请求失败(检查代理/VPN分流规则是否含sina): %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var ks []struct {
		Day    string `json:"day"`
		Open   string `json:"open"`
		High   string `json:"high"`
		Low    string `json:"low"`
		Close  string `json:"close"`
		Volume string `json:"volume"`
	}
	if err := json.Unmarshal(body, &ks); err != nil {
		return nil, fmt.Errorf("新浪K线返回非JSON（你的网络可能拦截了新浪行情，检查代理规则）: %v", err)
	}
	if len(ks) == 0 {
		return nil, fmt.Errorf("新浪K线为空")
	}
	// 2) 合成 trend（与东财趋势对齐：Open=bar开，Now=bar收，Vol为成交量股）
	var trend []FenshiTick
	var sumAmtPxVol float64
	var sumVolAll float64
	for _, k := range ks {
		o, _ := strconv.ParseFloat(k.Open, 64)
		h, _ := strconv.ParseFloat(k.High, 64)
		l, _ := strconv.ParseFloat(k.Low, 64)
		c, _ := strconv.ParseFloat(k.Close, 64)
		v, _ := strconv.ParseFloat(k.Volume, 64)
		sumVolAll += v
		sumAmtPxVol += (o + h + l + c) / 4 * v
		t := strings.Replace(k.Day, "2026-", "", 1)
		t = strings.Replace(t, "2025-", "", 1)
		if i2 := strings.Index(k.Day, " "); i2 >= 0 {
			t = k.Day[i2+1:]
		}
		trend = append(trend, FenshiTick{Time: t, Open: o, Now: c, High: h, Low: l, Vol: v, Amt: (o + h + l + c) / 4 * v, Avg: 0})
	}
	if len(trend) == 0 {
		return nil, fmt.Errorf("新浪K线解析为空")
	}
	// 3) 快照（昨收、名称、现价、总额）——hq.sinajs.cn GBK 编码
	snapURL := fmt.Sprintf("https://hq.sinajs.cn/list=%s", tcode)
	sreq, _ := http.NewRequest("GET", snapURL, nil)
	sreq.Header.Set("Referer", "https://finance.sina.com.cn/")
	sreq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/131.0.0.0 Safari/537.36")
	sresp, err := fenshiPlainClient.Do(sreq)
	if err != nil {
		return nil, fmt.Errorf("新浪快照请求失败: %v", err)
	}
	defer sresp.Body.Close()
	sbodyRaw, _ := io.ReadAll(sresp.Body)
	// v9f.15修复：新浪 hq 格式为 var hq_str_sh600519="名称,今开,昨收,现价,..."（逗号分隔，非~分隔）
	name := ""
	preClose, nowPx := 0.0, 0.0
	if sl := string(sbodyRaw); strings.Contains(sl, "=") {
		i3 := strings.Index(sl, "\"")
		j3 := strings.LastIndex(sl, "\"")
		if i3 >= 0 && j3 > i3 {
			fields := strings.Split(sl[i3+1:j3], ",")
			if len(fields) > 3 {
				nowPx, _ = strconv.ParseFloat(strings.TrimSpace(fields[3]), 64)
				preClose, _ = strconv.ParseFloat(strings.TrimSpace(fields[2]), 64)
			}
		}
	}
	if preClose <= 0 || nowPx <= 0 {
		return nil, fmt.Errorf("新浪快照解析失败")
	}
	_ = name
	// 4) 指标计算（同腾讯兜底口径）
	openPx := trend[0].Open
	highPx, lowPx := trend[0].High, trend[0].Low
	for _, tk := range trend {
		if tk.High > highPx {
			highPx = tk.High
		}
		if tk.Low < lowPx {
			lowPx = tk.Low
		}
	}
	vwap := 0.0
	if sumVolAll > 0 {
		vwap = sumAmtPxVol / sumVolAll
	}
	openPct := (openPx - preClose) / preClose * 100
	vwapDev := 0.0
	if vwap > 0 {
		vwapDev = (nowPx - vwap) / vwap * 100
	}
	n := len(trend)
	mom15 := 0.0 // 5分钟粒度：15分钟=3根
	if n > 3 && trend[n-4].Now > 0 {
		mom15 = (nowPx - trend[n-4].Now) / trend[n-4].Now * 100
	}
	fallFromHigh := (nowPx - highPx) / highPx * 100
	res := &FenshiAnalysisResult{
		Code: stockCode, Name: tcode,
		Price: nowPx, PrevClose: preClose,
		GapPct: openPct, ChangePct: (nowPx - preClose) / preClose * 100,
		AvgPrice: vwap, AboveVwapP: vwapDev,
		Mom15: mom15, VolumeRate: 0,
		PullbackPct: fallFromHigh, High: highPx, Low: lowPx,
		Trend: trend,
	}
	res.Signals = fenshiBuildSignals(res)
	res.Advice = fmt.Sprintf("[新浪源·5分钟级] %s 昨收%.2f 今开%.2f 现价%.2f（分钟粒度较粗、量比不可用，趋势判断仍有效）", tcode, preClose, openPx, nowPx)
	return res, nil
}
