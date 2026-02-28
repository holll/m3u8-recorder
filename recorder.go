package main

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path"
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
	stoppedAt    time.Time
	segmentsDone int64
	bytesDone    int64
	speedBps     float64
	lastStatAt   time.Time
	lastStatSize int64

	sessionDir  string
	filePrefix  string
	baseDirName string
}

type Recorder struct {
	downloadRoot string
	persistFile  string
	splitEvery   time.Duration
	requestUA    string

	scheduleEnabled bool
	scheduleStartM  int
	scheduleEndM    int

	for _, raw := range urls {
		u := strings.TrimSpace(raw)
		if u == "" {
			continue
		}
		if _, exists := r.rooms[u]; exists {
			continue
		}
		r.rooms[u] = &roomRecorder{url: u, state: StateIdle}
	}
}

func NewRecorder(downloadRoot string, splitSeconds int, requestUA string, scheduleStartM, scheduleEndM int, scheduleEnabled bool) *Recorder {
	r := &Recorder{
		downloadRoot:    downloadRoot,
		splitEvery:      time.Duration(splitSeconds) * time.Second,
		requestUA:       requestUA,
		scheduleEnabled: scheduleEnabled,
		scheduleStartM:  scheduleStartM,
		scheduleEndM:    scheduleEndM,
		rooms:           make(map[string]*roomRecorder),
	}
	if scheduleEnabled {
		go r.scheduleLoop()
	}
	return r
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
	CanStop         bool          `json:"can_stop"`
	CanStart        bool          `json:"can_start"`
}

type Status struct {
	DownloadRoot    string       `json:"download_root"`
	SplitSeconds    int          `json:"split_seconds"`
	ActiveCount     int          `json:"active_count"`
	ScheduleEnabled bool         `json:"schedule_enabled"`
	ScheduleStart   string       `json:"schedule_start"`
	ScheduleEnd     string       `json:"schedule_end"`
	ScheduleInRange bool         `json:"schedule_in_range"`
	Rooms           []RoomStatus `json:"rooms"`
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
			endAt := time.Now()
			if (room.state == StateIdle || room.state == StateError) && !room.stoppedAt.IsZero() {
				endAt = room.stoppedAt
			}
			uptime = int64(endAt.Sub(room.startedAt).Seconds())
			if uptime < 0 {
				uptime = 0
			}
		}
		speed := room.speedBps
		if room.state != StateRunning && room.state != StateStopping {
			speed = 0
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
			CanStop:         room.state == StateRunning || room.state == StateStopping,
			CanStart:        room.state == StateIdle || room.state == StateError,
		})
	}

	sort.Slice(rooms, func(i, j int) bool {
		return rooms[i].URL < rooms[j].URL
	})

	return Status{
		DownloadRoot:    r.downloadRoot,
		SplitSeconds:    int(r.splitEvery.Seconds()),
		ActiveCount:     active,
		ScheduleEnabled: r.scheduleEnabled,
		ScheduleStart:   minutesToHHMM(r.scheduleStartM),
		ScheduleEnd:     minutesToHHMM(r.scheduleEndM),
		ScheduleInRange: !r.scheduleEnabled || inScheduleWindow(time.Now(), r.scheduleStartM, r.scheduleEndM),
		Rooms:           rooms,
	}
}

func (r *Recorder) Start(m3u8URL string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if m3u8URL == "" {
		return errors.New("m3u8 url is empty")
	}

	room, ok := r.rooms[m3u8URL]
	if !ok {
		return errors.New("room not found, please add room first")
	}
	if room.state == StateRunning || room.state == StateStopping {
		return errors.New("this room is already running")
	}

	if err := r.startRoomLocked(room); err != nil {
		return err
	}
	return nil
}

func (r *Recorder) AddRoom(m3u8URL string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if m3u8URL == "" {
		return errors.New("m3u8 url is empty")
	}
	if _, ok := r.rooms[m3u8URL]; ok {
		return errors.New("room already exists")
	}

	r.rooms[m3u8URL] = &roomRecorder{
		state: StateIdle,
		url:   m3u8URL,
	}
	return nil
}

func (r *Recorder) startRoomLocked(room *roomRecorder) error {
	if room == nil || room.url == "" {
		return errors.New("room url is empty")
	}
	if room.state == StateRunning || room.state == StateStopping {
		return errors.New("this room is already running")
	}

	baseDirName := roomBaseName(room.url)
	baseDir := filepath.Join(r.downloadRoot, baseDirName)
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return fmt.Errorf("mkdir room dir: %w", err)
	}

	startTime := time.Now()
	startStamp := startTime.Format("20060102_150405")

	room.state = StateRunning
	room.lastErr = ""
	room.startedAt = startTime
	room.stoppedAt = time.Time{}
	room.segmentsDone = 0
	room.bytesDone = 0
	room.speedBps = 0
	room.lastStatAt = time.Time{}
	room.lastStatSize = 0
	room.sessionDir = baseDir
	room.filePrefix = startStamp + "_"
	room.baseDirName = baseDirName
	room.runCtx, room.cancelRun = context.WithCancel(context.Background())

	go r.runFFmpeg(room.runCtx, room.url, room.sessionDir, room.filePrefix)
	go r.monitorStats(room.runCtx, room.url, room.sessionDir, room.filePrefix)
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
	room.stoppedAt = time.Now()
	room.speedBps = 0
}

func (r *Recorder) setIdle(url string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	room, ok := r.rooms[url]
	if !ok {
		return
	}
	room.state = StateIdle
	if room.stoppedAt.IsZero() {
		room.stoppedAt = time.Now()
	}
	room.speedBps = 0
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
	now := time.Now()
	if !room.lastStatAt.IsZero() {
		dt := now.Sub(room.lastStatAt).Seconds()
		if dt > 0 {
			delta := totalBytes - room.lastStatSize
			if delta < 0 {
				delta = 0
			}
			room.speedBps = float64(delta) / dt
		}
	}
	room.lastStatAt = now
	room.lastStatSize = totalBytes
}

func (r *Recorder) runFFmpeg(ctx context.Context, m3u8URL, sessionDir, filePrefix string) {
	segmentPattern := filepath.Join(sessionDir, filePrefix+"%06d.ts")
	consecutiveFailures := 0
	for {
		if ctx.Err() == context.Canceled {
			r.setIdle(m3u8URL)
			log.Printf("[room=%s] ffmpeg stopped by user", m3u8URL)
			return
		}

		startNo := countExistingSegments(sessionDir, filePrefix) + 1
		args := []string{
			"-hide_banner",
			"-loglevel", "warning",
			"-user_agent", r.requestUA,
			"-rw_timeout", "15000000",
			"-i", m3u8URL,
			"-c", "copy",
			"-f", "segment",
			"-segment_time", strconv.Itoa(int(r.splitEvery.Seconds())),
			"-segment_start_number", strconv.FormatInt(startNo, 10),
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
			consecutiveFailures++
			if consecutiveFailures >= 8 {
				r.setError(m3u8URL, fmt.Errorf("start ffmpeg after %d retries: %w", consecutiveFailures, err))
				return
			case <-time.After(wait):
			}
			wait := retryBackoff(consecutiveFailures)
			log.Printf("[room=%s] start ffmpeg failed (attempt=%d): %v; retry in %s", m3u8URL, consecutiveFailures, err, wait)
			select {
			case <-ctx.Done():
				r.setIdle(m3u8URL)
				return
			case <-time.After(wait):
			}
			continue
		}
		log.Printf("[room=%s] ffmpeg started pid=%d output=%s pattern=%s start_no=%d", m3u8URL, cmd.Process.Pid, sessionDir, filepath.Base(segmentPattern), startNo)

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
			consecutiveFailures++
			if consecutiveFailures >= 8 {
				r.setError(m3u8URL, fmt.Errorf("read ffmpeg stderr after %d retries: %w", consecutiveFailures, scanner.Err()))
				return
			}
			wait := retryBackoff(consecutiveFailures)
			log.Printf("[room=%s] ffmpeg stderr read failed: %v; retry in %s", m3u8URL, scanner.Err(), wait)
			select {
			case <-ctx.Done():
				r.setIdle(m3u8URL)
				return
			case <-time.After(wait):
			}
			continue
		}
		if waitErr != nil {
			consecutiveFailures++
			if consecutiveFailures >= 8 {
				if len(lastErrLines) > 0 {
					r.setError(m3u8URL, fmt.Errorf("ffmpeg exited after %d retries: %v; last logs: %s", consecutiveFailures, waitErr, strings.Join(lastErrLines, " | ")))
					return
				}
				r.setError(m3u8URL, fmt.Errorf("ffmpeg exited after %d retries: %w", consecutiveFailures, waitErr))
				return
			}
			wait := retryBackoff(consecutiveFailures)
			if len(lastErrLines) > 0 {
				log.Printf("[room=%s] ffmpeg exited: %v; retry in %s; last logs: %s", m3u8URL, waitErr, wait, strings.Join(lastErrLines, " | "))
			} else {
				log.Printf("[room=%s] ffmpeg exited: %v; retry in %s", m3u8URL, waitErr, wait)
			}
			select {
			case <-ctx.Done():
				r.setIdle(m3u8URL)
				return
			case <-time.After(wait):
			}
			continue
		}

		// ffmpeg 正常结束（直播源主动结束）
		r.setIdle(m3u8URL)
		return
	}
}

func countExistingSegments(dir, prefix string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if prefix != "" && !strings.HasPrefix(name, prefix) {
			return nil
		}
		if strings.HasSuffix(strings.ToLower(name), ".ts") {
			n++
		}
		return nil
	})
	return n
}

func retryBackoff(failures int) time.Duration {
	if failures <= 1 {
		return 2 * time.Second
	}
	if failures == 2 {
		return 4 * time.Second
	}
	if failures == 3 {
		return 8 * time.Second
	}
	if failures == 4 {
		return 15 * time.Second
	}
	return 20 * time.Second
}

func (r *Recorder) monitorStats(ctx context.Context, m3u8URL, dir, prefix string) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.setIdle(m3u8URL)
			return
		case <-ticker.C:
			segCount, totalBytes := scanDirStats(dir, prefix)
			r.setStats(m3u8URL, segCount, totalBytes)
		}
	}
}

func scanDirStats(dir, prefix string) (segCount int64, totalBytes int64) {
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if prefix != "" && !strings.HasPrefix(name, prefix) {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		totalBytes += info.Size()
		if strings.HasSuffix(strings.ToLower(name), ".ts") {
			segCount++
		}
		return nil
	})
	return
}

func (r *Recorder) scheduleLoop() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		r.applyScheduleOnce(time.Now())
		<-ticker.C
	}
}

func (r *Recorder) applyScheduleOnce(now time.Time) {
	if !r.scheduleEnabled {
		return
	}
	inWindow := inScheduleWindow(now, r.scheduleStartM, r.scheduleEndM)

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, room := range r.rooms {
		if inWindow {
			if room.state == StateIdle {
				if err := r.startRoomLocked(room); err != nil {
					room.state = StateError
					room.lastErr = "auto start failed: " + err.Error()
					room.stoppedAt = time.Now()
					room.speedBps = 0
				}
			}
			continue
		}

		if room.state == StateRunning {
			room.state = StateStopping
			if room.cancelRun != nil {
				room.cancelRun()
			}
		}
	}
}

func inScheduleWindow(now time.Time, startM, endM int) bool {
	if startM == endM {
		return true
	}
	cur := now.Hour()*60 + now.Minute()
	if startM < endM {
		return cur >= startM && cur < endM
	}
	return cur >= startM || cur < endM
}

func minutesToHHMM(m int) string {
	if m < 0 {
		m = 0
	}
	m = m % (24 * 60)
	hh := m / 60
	mm := m % 60
	return fmt.Sprintf("%02d:%02d", hh, mm)
}

func roomBaseName(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err == nil {
		b := strings.TrimSpace(path.Base(strings.TrimSuffix(u.Path, "/")))
		if b != "" && b != "." && b != "/" {
			return sanitizeName(b)
		}
		if host := sanitizeName(u.Hostname()); host != "" {
			return host
		}
	}
	return "room_" + shortHash(rawURL)
}

func sanitizeName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_")
	s = replacer.Replace(s)
	s = strings.Trim(s, " .")
	if s == "" {
		return ""
	}
	return s
}

func shortHash(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:])[:10]
}
