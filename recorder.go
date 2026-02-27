package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type RecorderState string

const (
	StateIdle     RecorderState = "idle"
	StateRunning  RecorderState = "running"
	StateStopping RecorderState = "stopping"
	StateError    RecorderState = "error"
)

type Recorder struct {
	downloadRoot string
	splitEvery   time.Duration

	mu      sync.Mutex
	state   RecorderState
	lastErr string

	runCtx    context.Context
	cancelRun context.CancelFunc

	currentURL string

	// stats
	startedAt    time.Time
	segmentsDone int64
	bytesDone    int64

	// de-dup
	seen map[string]struct{}
}

func NewRecorder(downloadRoot string, splitSeconds int) *Recorder {
	return &Recorder{
		downloadRoot: downloadRoot,
		splitEvery:   time.Duration(splitSeconds) * time.Second,
		state:        StateIdle,
		seen:         make(map[string]struct{}),
	}
}

type Status struct {
	State         RecorderState `json:"state"`
	Error         string        `json:"error"`
	URL           string        `json:"url"`
	StartedAt     string        `json:"started_at"`
	SegmentsDone  int64         `json:"segments_done"`
	BytesDone     int64         `json:"bytes_done"`
	DownloadRoot  string        `json:"download_root"`
	SplitSeconds  int           `json:"split_seconds"`
	UptimeSeconds int64         `json:"uptime_seconds"`
}

func (r *Recorder) GetStatus() Status {
	r.mu.Lock()
	defer r.mu.Unlock()

	var started string
	var uptime int64
	if !r.startedAt.IsZero() {
		started = r.startedAt.Format(time.RFC3339)
		uptime = int64(time.Since(r.startedAt).Seconds())
	}

	return Status{
		State:         r.state,
		Error:         r.lastErr,
		URL:           r.currentURL,
		StartedAt:     started,
		SegmentsDone:  r.segmentsDone,
		BytesDone:     r.bytesDone,
		DownloadRoot:  r.downloadRoot,
		SplitSeconds:  int(r.splitEvery.Seconds()),
		UptimeSeconds: uptime,
	}
}

func (r *Recorder) Start(m3u8URL string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if m3u8URL == "" {
		return errors.New("m3u8 url is empty")
	}
	if r.state == StateRunning {
		return errors.New("already running")
	}

	// reset
	r.state = StateRunning
	r.lastErr = ""
	r.currentURL = m3u8URL
	r.startedAt = time.Now()
	r.segmentsDone = 0
	r.bytesDone = 0
	r.seen = make(map[string]struct{})

	r.runCtx, r.cancelRun = context.WithCancel(context.Background())

	go r.loop(r.runCtx, m3u8URL)

	return nil
}

func (r *Recorder) Stop() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.state != StateRunning {
		return errors.New("not running")
	}
	r.state = StateStopping
	if r.cancelRun != nil {
		r.cancelRun()
	}
	return nil
}

func (r *Recorder) setError(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state = StateError
	r.lastErr = err.Error()
}

func (r *Recorder) setIdle() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state = StateIdle
}

func (r *Recorder) incStats(segBytes int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.segmentsDone++
	r.bytesDone += segBytes
}

func (r *Recorder) loop(ctx context.Context, m3u8URL string) {
	client := &http.Client{
		Timeout: 20 * time.Second,
	}

	// session dir：按 url hash
	sessionID := shortHash(m3u8URL)
	sessionDir := filepath.Join(r.downloadRoot, time.Now().Format("20060102_150405")+"_"+sessionID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		r.setError(fmt.Errorf("mkdir session dir: %w", err))
		return
	}

	chunkStart := time.Now()
	chunkIndex := 1
	chunkDir := filepath.Join(sessionDir, fmt.Sprintf("chunk_%04d", chunkIndex))
	if err := os.MkdirAll(chunkDir, 0o755); err != nil {
		r.setError(fmt.Errorf("mkdir chunk dir: %w", err))
		return
	}

	// poll interval: fallback 2s; update after parsing playlist
	pollEvery := 2 * time.Second

	for {
		select {
		case <-ctx.Done():
			r.setIdle()
			return
		default:
		}

		playlistBody, err := fetchText(ctx, client, m3u8URL)
		if err != nil {
			r.setError(fmt.Errorf("fetch playlist: %w", err))
			return
		}

		pl, err := ParseM3U8(m3u8URL, playlistBody)
		if err != nil {
			r.setError(fmt.Errorf("parse playlist: %w", err))
			return
		}
		if pl.TargetDuration > 0 {
			// HLS 建议：用 targetduration 的一半左右轮询更稳
			pollEvery = time.Duration(pl.TargetDuration) * time.Second / 2
			if pollEvery < 1*time.Second {
				pollEvery = 1 * time.Second
			}
			if pollEvery > 10*time.Second {
				pollEvery = 10 * time.Second
			}
		}

		// 切割
		if time.Since(chunkStart) >= r.splitEvery {
			chunkIndex++
			chunkStart = time.Now()
			chunkDir = filepath.Join(sessionDir, fmt.Sprintf("chunk_%04d", chunkIndex))
			if err := os.MkdirAll(chunkDir, 0o755); err != nil {
				r.setError(fmt.Errorf("mkdir chunk dir: %w", err))
				return
			}
		}

		// 下载新分片
		for _, seg := range pl.Segments {
			segKey := seg.URL
			if segKey == "" {
				continue
			}

			r.mu.Lock()
			_, ok := r.seen[segKey]
			if !ok {
				r.seen[segKey] = struct{}{}
			}
			r.mu.Unlock()

			if ok {
				continue
			}

			// 文件名：媒体序号_时间戳.ts（若无序号则用hash）
			name := seg.Filename()
			outPath := filepath.Join(chunkDir, name)

			n, err := downloadFile(ctx, client, seg.URL, outPath)
			if err != nil {
				r.setError(fmt.Errorf("download segment: %w", err))
				return
			}
			r.incStats(n)
		}

		select {
		case <-ctx.Done():
			r.setIdle()
			return
		case <-time.After(pollEvery):
		}
	}
}

func fetchText(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("http %d", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func downloadFile(ctx context.Context, client *http.Client, url, outPath string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return 0, fmt.Errorf("segment http %d", resp.StatusCode)
	}

	tmp := outPath + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	n, err := io.Copy(f, resp.Body)
	if err != nil {
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, outPath); err != nil {
		return 0, err
	}
	return n, nil
}

func shortHash(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:])[:10]
}
