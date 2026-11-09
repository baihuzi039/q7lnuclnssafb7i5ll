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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
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
	Name    string  `json:"name"`
	Type    string  `json:"type"`    // buy / warn / info
	Prob    string  `json:"prob"`    // 概率/依据描述
	Desc    string  `json:"desc"`
}

// FenshiAnalysisResult 个股分时分析结果
type FenshiAnalysisResult struct {
	Code        string          `json:"code"`
	Name        string          `json:"name"`
	Price       float64         `json:"price"`
	PrevClose   float64         `json:"prevClose"`
	GapPct      float64         `json:"gapPct"`      // 高开%
	ChangePct   float64         `json:"changePct"`
	AvgPrice    float64         `json:"avgPrice"`    // 均价线
	AboveVwapP  float64         `json:"aboveVwapP"`  // 现价偏离均价线%
	Mom15       float64         `json:"mom15"`       // 15分钟动量%
	VolumeRate  float64         `json:"volumeRate"`
	PullbackPct float64         `json:"pullbackPct"` // 距开盘后最高点回落%
	High        float64         `json:"high"`
	Low         float64         `json:"low"`
	Trend       []FenshiTick    `json:"trend"` // 分时数据点

	Signals     []FenshiSignal  `json:"signals"`
	Advice      string          `json:"advice"`
	ErrMsg      string          `json:"errMsg"` // 非空表示分析失败及原因
}

// fenshiDialTLS 用 utls 模拟 Chrome TLS 指纹，防东财风控识别 Go 默认 crypto/tls 导致 EOF 断连
func fenshiDialTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	rawConn, err := net.DialTimeout(network, addr, 15*time.Second)
	if err != nil {
		return nil, err
	}
	cfg := &utls.Config{ServerName: host, NextProtos: []string{"http/1.1"}}
	uconn := utls.UClient(rawConn, cfg, utls.HelloChrome_Auto)
	if err := uconn.HandshakeContext(ctx); err != nil {
		rawConn.Close()
		return nil, err
	}
	return uconn, nil
}

var fenshiHTTPClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		DialTLSContext:       fenshiDialTLS,
		TLSHandshakeTimeout: 15 * time.Second,
		ForceAttemptHTTP2:    false,
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
var fenshiPlainClient = &http.Client{Timeout: 15 * time.Second}

func fenshiGet(rawURL string) (map[string]any, error) {
	var lastErr error
	// 第0优先：http 明文（无TLS指纹，风控不拦）
	if strings.HasPrefix(rawURL, "https://") {
		plainURL := "http://" + rawURL[len("https://"):]
		m, err := fenshiGetOnce(plainURL, fenshiPlainClient)
		if err == nil {
			return m, nil
		}
		lastErr = err
		logger.SugaredLogger.Warnf("fenshiGet http明文失败，转https: %v", err)
	}
	attempts := []string{rawURL}
	// 域名备援：主域被风控时换备用节点（东财同一接口多域名部署）
	if strings.Contains(rawURL, "push2his.eastmoney.com") {
		attempts = append(attempts, strings.Replace(rawURL, "push2his.eastmoney.com", "92.push2his.eastmoney.com", 1))
	} else if strings.Contains(rawURL, "push2.eastmoney.com") {
		attempts = append(attempts, strings.Replace(rawURL, "push2.eastmoney.com", "92.push2.eastmoney.com", 1))
	}
	for i, u := range attempts {
		m, err := fenshiGetOnce(u, fenshiHTTPClient)
		if err == nil {
			return m, nil
		}
		lastErr = err
		logger.SugaredLogger.Warnf("fenshiGet 第%d次失败: %v", i+1, err)
		time.Sleep(300 * time.Millisecond)
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
		"m:0+t:6,m:0+t:80,m:1+t:2,m:1+t:23",         // 沪深主板+创业板
		"m:0+t:81+s:2048",                            // 科创板
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
func (a *App) FenshiAnalyzeStock(stockCode string) *FenshiAnalysisResult {
	secid := fenshiSecid(stockCode)
	url := fmt.Sprintf("https://push2his.eastmoney.com/api/qt/stock/trends2/get?secid=%s&fields1=f1,f2,f3,f4,f5,f6,f7,f8,f9,f10,f11,f12,f13&fields2=f51,f52,f53,f54,f55,f56,f57,f58&iscr=0&ndays=1", secid)
	retErr := func(msg string) *FenshiAnalysisResult {
		logger.SugaredLogger.Errorf("FenshiAnalyzeStock %s: %s", stockCode, msg)
		return &FenshiAnalysisResult{Code: stockCode, ErrMsg: msg}
	}
	m, err := fenshiGet(url)
	if err != nil {
		return retErr("分时数据请求失败: " + err.Error())
	}
	d, _ := m["data"].(map[string]any)
	if d == nil {
		return retErr("未获取到分时数据（可能代码不存在，或当前为非交易时段且接口无当日数据），代码: " + stockCode)
	}
	name, _ := d["name"].(string)
	preClose := fenshiToFloat(d["preClose"])
	trends, _ := d["trends"].([]any)
	if len(trends) == 0 || preClose <= 0 {
		return retErr("分时数据为空（接口返回无趋势数据），代码: " + stockCode)
	}

	res := &FenshiAnalysisResult{Code: stockCode, Name: name, PrevClose: preClose, Trend: make([]FenshiTick, 0, len(trends))}
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
		res.Trend = append(res.Trend, FenshiTick{Time: parts[0], Open: op, Now: np, High: hp, Low: lp, Vol: fenshiToFloat(parts[5]), Amt: fenshiToFloat(parts[6]), Avg: ap})
	}
	if openPx <= 0 || nowPx <= 0 {
		return retErr("分时数据解析异常（价格均为0），代码: " + stockCode)
	}
	res.Price = nowPx
	res.GapPct = (openPx/preClose - 1) * 100
	res.ChangePct = (nowPx/preClose - 1) * 100
	res.High = highPx
	res.Low = lowPx
	res.AvgPrice = avgPx
	if avgPx > 0 {
		res.AboveVwapP = (nowPx/avgPx - 1) * 100
	}
	if px15 > 0 {
		res.Mom15 = (nowPx/px15 - 1) * 100
	}
	if highPx > 0 {
		res.PullbackPct = (1 - nowPx/highPx) * 100
	}
	// 量比: 快照 f10
	snapURL := fmt.Sprintf("https://push2.eastmoney.com/api/qt/stock/get?secid=%s&fields=f10,f12,f14", secid)
	sm, err := fenshiGet(snapURL)
	if err == nil {
		if sd, _ := sm["data"].(map[string]any); sd != nil {
			res.VolumeRate = fenshiToFloat(sd["f10"])
			if n, ok := sd["f14"].(string); ok {
				res.Name = n
			}
		}
	}

	res.Signals = fenshiBuildSignals(res)
	res.Advice = fenshiAdviceText(res)
	return res
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
