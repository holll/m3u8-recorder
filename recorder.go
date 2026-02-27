package main

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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

	sessionDir string
}

type Recorder struct {
	downloadRoot string
	splitEvery   time.Duration
	requestUA    string

	mu    sync.Mutex
	rooms map[string]*roomRecorder
}

func NewRecorder(downloadRoot string, splitSeconds int, requestUA string) *Recorder {
	return &Recorder{
		downloadRoot: downloadRoot,
		splitEvery:   time.Duration(splitSeconds) * time.Second,
		requestUA:    requestUA,
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

	sessionID := shortHash(m3u8URL)
	sessionDir := filepath.Join(r.downloadRoot, time.Now().Format("20060102_150405")+"_"+sessionID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return fmt.Errorf("mkdir session dir: %w", err)
	}

	room := &roomRecorder{
		state:      StateRunning,
		lastErr:    "",
		url:        m3u8URL,
		startedAt:  time.Now(),
		sessionDir: sessionDir,
	}
	room.runCtx, room.cancelRun = context.WithCancel(context.Background())
	r.rooms[m3u8URL] = room

	go r.runFFmpeg(room.runCtx, m3u8URL, sessionDir)
	go r.monitorStats(room.runCtx, m3u8URL, sessionDir)

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

func (r *Recorder) setStats(url string, segCount, totalBytes int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	room, ok := r.rooms[url]
	if !ok {
		return
	}
	room.segmentsDone = segCount
	room.bytesDone = totalBytes
}

func (r *Recorder) runFFmpeg(ctx context.Context, m3u8URL, sessionDir string) {
	segmentPattern := filepath.Join(sessionDir, "chunk_%06d.ts")
	args := []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-user_agent", r.requestUA,
		"-i", m3u8URL,
		"-c", "copy",
		"-f", "segment",
		"-segment_time", strconv.Itoa(int(r.splitEvery.Seconds())),
		"-reset_timestamps", "1",
		segmentPattern,
	}

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		r.setError(m3u8URL, fmt.Errorf("ffmpeg stderr pipe: %w", err))
		return
	}

	if err := cmd.Start(); err != nil {
		r.setError(m3u8URL, fmt.Errorf("start ffmpeg: %w", err))
		return
	}
	log.Printf("[room=%s] ffmpeg started pid=%d output=%s", m3u8URL, cmd.Process.Pid, sessionDir)

	var lastErrLines []string
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		log.Printf("[room=%s] ffmpeg: %s", m3u8URL, line)
		lastErrLines = append(lastErrLines, line)
		if len(lastErrLines) > 8 {
			lastErrLines = lastErrLines[1:]
		}
	}

	waitErr := cmd.Wait()
	if ctx.Err() == context.Canceled {
		r.setIdle(m3u8URL)
		log.Printf("[room=%s] ffmpeg stopped by user", m3u8URL)
		return
	}
	if scanner.Err() != nil {
		r.setError(m3u8URL, fmt.Errorf("read ffmpeg stderr: %w", scanner.Err()))
		return
	}
	if waitErr != nil {
		if len(lastErrLines) > 0 {
			r.setError(m3u8URL, fmt.Errorf("ffmpeg exited: %v; last logs: %s", waitErr, strings.Join(lastErrLines, " | ")))
			return
		}
		r.setError(m3u8URL, fmt.Errorf("ffmpeg exited: %w", waitErr))
		return
	}

	r.setIdle(m3u8URL)
}

func (r *Recorder) monitorStats(ctx context.Context, m3u8URL, dir string) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.setIdle(m3u8URL)
			return
		case <-ticker.C:
			segCount, totalBytes := scanDirStats(dir)
			r.setStats(m3u8URL, segCount, totalBytes)
		}
	}
}

func scanDirStats(dir string) (segCount int64, totalBytes int64) {
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		totalBytes += info.Size()
		if strings.HasSuffix(strings.ToLower(d.Name()), ".ts") {
			segCount++
		}
		return nil
	})
	return
}

func shortHash(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:])[:10]
}
