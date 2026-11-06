package data

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

// ProgressCallback 周期性回调（每 500ms 或下载完成时调用）。
// downloaded: 已下载字节数; total: 总字节数;
// percentage: 0-100; currentSpeed: 瞬时速度(bytes/s); avgSpeed: 平均速度(bytes/s)。
type ProgressCallback func(downloaded, total int64, percentage, currentSpeed, avgSpeed float64)

const progressEmitInterval = 500 * time.Millisecond

// progressReader 包装 io.ReadCloser，在读取过程中追踪进度并周期性调用回调。
// 慢速监测：平均速度连续 slowSlowCount 个采样周期低于 minSpeed 字节/秒时返回 ErrTooSlow，
// 上层捕获后切换下一个源断点续传。
type progressReader struct {
	reader        io.ReadCloser
	total         int64
	downloaded    int64
	callback      ProgressCallback
	startTime     time.Time
	lastEmitTime  time.Time
	lastEmitBytes int64
	minSpeed      float64 // 慢速阈值 bytes/s；0=不启用
	slowLimit     int     // 连续多少个采样周期低于阈值才中止
	slowCount     int
}

// ErrTooSlow 下载速度持续低于阈值，由上层切换源
var ErrTooSlow = fmt.Errorf("download speed too slow, switching source")

func newProgressReader(reader io.ReadCloser, total int64, cb ProgressCallback, startOffset int64, minSpeedKB float64, slowLimit int) *progressReader {
	now := time.Now()
	ms := minSpeedKB * 1024
	if ms > 0 && slowLimit <= 0 {
		slowLimit = 4 // 默认连续4个采样周期（约2秒）过慢才切换
	}
	return &progressReader{
		reader:       reader,
		total:        total,
		callback:     cb,
		startTime:    now,
		lastEmitTime: now,
		downloaded:   startOffset,
		minSpeed:     ms,
		slowLimit:    slowLimit,
	}
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	if n > 0 {
		pr.downloaded += int64(n)
	}

	now := time.Now()
	shouldEmit := now.Sub(pr.lastEmitTime) >= progressEmitInterval || err != nil
	if shouldEmit && pr.callback != nil {
		elapsed := now.Sub(pr.lastEmitTime).Seconds()
		currentSpeed := float64(0)
		if elapsed > 0 {
			currentSpeed = float64(pr.downloaded-pr.lastEmitBytes) / elapsed
		}
		avgElapsed := now.Sub(pr.startTime).Seconds()
		avgSpeed := float64(0)
		if avgElapsed > 0 {
			avgSpeed = float64(pr.downloaded) / avgElapsed
		}
		percentage := float64(0)
		if pr.total > 0 {
			percentage = float64(pr.downloaded) * 100 / float64(pr.total)
			if percentage > 100 {
				percentage = 100
			}
		}
		pr.callback(pr.downloaded, pr.total, percentage, currentSpeed, avgSpeed)
		pr.lastEmitTime = now
		pr.lastEmitBytes = pr.downloaded
		// 慢速监测：平均速度持续低于阈值 → 中止，由上层切换源断点续传
		if pr.minSpeed > 0 && avgSpeed < pr.minSpeed {
			pr.slowCount++
			if pr.slowCount >= pr.slowLimit {
				return n, ErrTooSlow
			}
		} else {
			pr.slowCount = 0
		}
	}

	return n, err
}

func (pr *progressReader) Close() error {
	return pr.reader.Close()
}

// DownloadWithProgress 从 url 下载到 tmpPath，每 500ms 调用 progressCB。
// totalSize 为预期大小（来自 GitHub API asset.Size），为 0 时用 Content-Length。
// 使用 net/http 直接请求（非 resty），避免 resty 重试干扰进度追踪。
// minSpeedKB > 0 时启用慢速监测：平均速度持续低于阈值（连续 slowStreak 次）则中止返回错误，
// 由上层切换下一个源并从已下载字节处断点续传。
func DownloadWithProgress(ctx context.Context, url string, tmpPath string, totalSize int64, progressCB ProgressCallback, minSpeedKB float64, slowStreak int) error {
	// 断点续传：已有部分文件则从尾部继续
	var existing int64
	if fi, statErr := os.Stat(tmpPath); statErr == nil {
		existing = fi.Size()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("User-Agent", "go-stock-updater")
	if existing > 0 && totalSize > 0 && existing < totalSize {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", existing))
	}
	startOffset := existing

	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          5,
		MaxIdleConnsPerHost:   2,
		MaxConnsPerHost:       2,
		IdleConnTimeout:       120 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
		// 下载走 GitHub 镜像源（gh-proxy 等国内可达反代节点），保持直连，不使用配置的代理
		Proxy: nil,
	}
	client := &http.Client{Transport: transport}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("下载请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("下载失败, HTTP状态: %d", resp.StatusCode)
	}

	total := totalSize
	if total <= 0 {
		total = resp.ContentLength + existing
	}

	// 续传时追加写入；全新下载则重建文件
	var out *os.File
	if startOffset > 0 && (resp.StatusCode == http.StatusPartialContent) {
		out, err = os.OpenFile(tmpPath, os.O_APPEND|os.O_WRONLY, 0644)
	} else {
		out, err = os.Create(tmpPath)
		startOffset = 0
	}
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	defer out.Close()

	pr := newProgressReader(resp.Body, total, progressCB, startOffset, minSpeedKB, slowStreak)
	_, err = io.Copy(out, pr)
	if err != nil {
		if errors.Is(err, ErrTooSlow) {
			return err // 原样上抛，上层据此切换源断点续传
		}
		return fmt.Errorf("下载写入失败: %w", err)
	}

	// 发射最终进度事件确保用户看到 100%
	if progressCB != nil {
		avgElapsed := time.Since(pr.startTime).Seconds()
		avgSpeed := float64(0)
		if avgElapsed > 0 {
			avgSpeed = float64(pr.downloaded) / avgElapsed
		}
		percentage := float64(100)
		if total > 0 && pr.downloaded < total {
			percentage = float64(pr.downloaded) * 100 / float64(total)
		}
		progressCB(pr.downloaded, total, percentage, 0, avgSpeed)
	}

	return nil
}
