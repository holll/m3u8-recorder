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
	"sort"
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

type roomRecorder struct {
	state   RecorderState
	lastErr string

	runCtx    context.Context
	cancelRun context.CancelFunc

	url string

	startedAt    time.Time
	segmentsDone int64
	bytesDone    int64

	seen map[string]struct{}
}

type Recorder struct {
	downloadRoot string
	splitEvery   time.Duration

	mu    sync.Mutex
	rooms map[string]*roomRecorder
}

func NewRecorder(downloadRoot string, splitSeconds int) *Recorder {
	return &Recorder{
		downloadRoot: downloadRoot,
		splitEvery:   time.Duration(splitSeconds) * time.Second,
		rooms:        make(map[string]*roomRecorder),
	}
}

type RoomStatus struct {
	State           RecorderState `json:"state"`
	Error           string        `json:"error"`
	URL             string        `json:"url"`
	StartedAt       string        `json:"started_at"`
	SegmentsDone    int64         `json:"segments_done"`
	BytesDone       int64         `json:"bytes_done"`
	UptimeSeconds   int64         `json:"uptime_seconds"`
	SpeedBytesPerS  float64       `json:"speed_bytes_per_s"`
	SpeedKBytesPerS float64       `json:"speed_kb_per_s"`
}

type Status struct {
	DownloadRoot string       `json:"download_root"`
	SplitSeconds int          `json:"split_seconds"`
	ActiveCount  int          `json:"active_count"`
	Rooms        []RoomStatus `json:"rooms"`
}

func (r *Recorder) GetStatus() Status {
	r.mu.Lock()
	defer r.mu.Unlock()

	rooms := make([]RoomStatus, 0, len(r.rooms))
	active := 0
	for _, room := range r.rooms {
		var started string
		var uptime int64
		if !room.startedAt.IsZero() {
			started = room.startedAt.Format(time.RFC3339)
			uptime = int64(time.Since(room.startedAt).Seconds())
		}
		speed := 0.0
		if uptime > 0 {
			speed = float64(room.bytesDone) / float64(uptime)
		}
		if room.state == StateRunning {
			active++
		}
		rooms = append(rooms, RoomStatus{
			State:           room.state,
			Error:           room.lastErr,
			URL:             room.url,
			StartedAt:       started,
			SegmentsDone:    room.segmentsDone,
			BytesDone:       room.bytesDone,
			UptimeSeconds:   uptime,
			SpeedBytesPerS:  speed,
			SpeedKBytesPerS: speed / 1024,
		})
	}

	sort.Slice(rooms, func(i, j int) bool {
		return rooms[i].URL < rooms[j].URL
	})

	return Status{
		DownloadRoot: r.downloadRoot,
		SplitSeconds: int(r.splitEvery.Seconds()),
		ActiveCount:  active,
		Rooms:        rooms,
	}
}

func (r *Recorder) Start(m3u8URL string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if m3u8URL == "" {
		return errors.New("m3u8 url is empty")
	}
	if room, ok := r.rooms[m3u8URL]; ok {
		if room.state == StateRunning || room.state == StateStopping {
			return errors.New("this room is already running")
		}
	}

	room := &roomRecorder{
		state:     StateRunning,
		lastErr:   "",
		url:       m3u8URL,
		startedAt: time.Now(),
		seen:      make(map[string]struct{}),
	}
	room.runCtx, room.cancelRun = context.WithCancel(context.Background())
	r.rooms[m3u8URL] = room

	go r.loop(room.runCtx, m3u8URL)

	return nil
}

func (r *Recorder) Stop(m3u8URL string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if m3u8URL == "" {
		stopped := 0
		for _, room := range r.rooms {
			if room.state == StateRunning {
				room.state = StateStopping
				if room.cancelRun != nil {
					room.cancelRun()
				}
				stopped++
			}
		}
		if stopped == 0 {
			return errors.New("no running rooms")
		}
		return nil
	}

	room, ok := r.rooms[m3u8URL]
	if !ok {
		return errors.New("room not found")
	}
	if room.state != StateRunning {
		return errors.New("room is not running")
	}
	room.state = StateStopping
	if room.cancelRun != nil {
		room.cancelRun()
	}
	return nil
}

func (r *Recorder) setError(url string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	room, ok := r.rooms[url]
	if !ok {
		return
	}
	room.state = StateError
	room.lastErr = err.Error()
}

func (r *Recorder) setIdle(url string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	room, ok := r.rooms[url]
	if !ok {
		return
	}
	room.state = StateIdle
}

func (r *Recorder) incStats(url string, segBytes int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	room, ok := r.rooms[url]
	if !ok {
		return
	}
	room.segmentsDone++
	room.bytesDone += segBytes
}

func (r *Recorder) markSeen(url string, segKey string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	room, ok := r.rooms[url]
	if !ok {
		return false
	}
	_, exists := room.seen[segKey]
	if exists {
		return false
	}
	room.seen[segKey] = struct{}{}
	return true
}

func (r *Recorder) loop(ctx context.Context, m3u8URL string) {
	client := &http.Client{Timeout: 20 * time.Second}

	sessionID := shortHash(m3u8URL)
	sessionDir := filepath.Join(r.downloadRoot, time.Now().Format("20060102_150405")+"_"+sessionID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		r.setError(m3u8URL, fmt.Errorf("mkdir session dir: %w", err))
		return
	}

	chunkStart := time.Now()
	chunkIndex := 1
	chunkDir := filepath.Join(sessionDir, fmt.Sprintf("chunk_%04d", chunkIndex))
	if err := os.MkdirAll(chunkDir, 0o755); err != nil {
		r.setError(m3u8URL, fmt.Errorf("mkdir chunk dir: %w", err))
		return
	}

	pollEvery := 2 * time.Second

	for {
		select {
		case <-ctx.Done():
			r.setIdle(m3u8URL)
			return
		default:
		}

		playlistBody, err := fetchText(ctx, client, m3u8URL)
		if err != nil {
			r.setError(m3u8URL, fmt.Errorf("fetch playlist: %w", err))
			return
		}

		pl, err := ParseM3U8(m3u8URL, playlistBody)
		if err != nil {
			r.setError(m3u8URL, fmt.Errorf("parse playlist: %w", err))
			return
		}
		if pl.TargetDuration > 0 {
			pollEvery = time.Duration(pl.TargetDuration) * time.Second / 2
			if pollEvery < 1*time.Second {
				pollEvery = 1 * time.Second
			}
			if pollEvery > 10*time.Second {
				pollEvery = 10 * time.Second
			}
		}

		if time.Since(chunkStart) >= r.splitEvery {
			chunkIndex++
			chunkStart = time.Now()
			chunkDir = filepath.Join(sessionDir, fmt.Sprintf("chunk_%04d", chunkIndex))
			if err := os.MkdirAll(chunkDir, 0o755); err != nil {
				r.setError(m3u8URL, fmt.Errorf("mkdir chunk dir: %w", err))
				return
			}
		}

		for _, seg := range pl.Segments {
			segKey := seg.URL
			if segKey == "" {
				continue
			}

			if !r.markSeen(m3u8URL, segKey) {
				continue
			}

			name := seg.Filename()
			outPath := filepath.Join(chunkDir, name)

			n, err := downloadFile(ctx, client, seg.URL, outPath)
			if err != nil {
				r.setError(m3u8URL, fmt.Errorf("download segment: %w", err))
				return
			}
			r.incStats(m3u8URL, n)
		}

		select {
		case <-ctx.Done():
			r.setIdle(m3u8URL)
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
