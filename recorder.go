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
	roomsFile    string
	splitEvery   time.Duration
	requestUA    string

	scheduleEnabled bool
	scheduleStartM  int
	scheduleEndM    int

	mu    sync.Mutex
	rooms map[string]*roomRecorder
}

func NewRecorder(downloadRoot string, splitSeconds int, requestUA string, scheduleStartM, scheduleEndM int, scheduleEnabled bool) *Recorder {
	r := &Recorder{
		downloadRoot:    downloadRoot,
		roomsFile:       filepath.Join(downloadRoot, "rooms.json"),
		splitEvery:      time.Duration(splitSeconds) * time.Second,
		requestUA:       requestUA,
		scheduleEnabled: scheduleEnabled,
		scheduleStartM:  scheduleStartM,
		scheduleEndM:    scheduleEndM,
		rooms:           make(map[string]*roomRecorder),
	}
	r.loadRooms()
	go r.convertExistingTSOnStartup()
	time.Sleep(5 * time.Second)
	if scheduleEnabled {
		go r.scheduleLoop()
	}
	return r
}

func (r *Recorder) convertExistingTSOnStartup() {
	tsFiles := make([]string, 0, 128)
	_ = filepath.WalkDir(r.downloadRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".ts") {
			return nil
		}
		tsFiles = append(tsFiles, path)
		return nil
	})

	if len(tsFiles) == 0 {
		return
	}

	sort.Strings(tsFiles)
	log.Printf("startup scan found %d ts files, converting...", len(tsFiles))

	converted := 0
	for _, tsPath := range tsFiles {
		mp4Path := strings.TrimSuffix(tsPath, filepath.Ext(tsPath)) + ".mp4"
		if _, err := os.Stat(mp4Path); err == nil {
			continue
		}
		if err := remuxTS2MP4(context.Background(), tsPath, mp4Path); err != nil {
			log.Printf("startup convert %s -> %s failed: %v", filepath.Base(tsPath), filepath.Base(mp4Path), err)
			continue
		}
		_ = os.Remove(tsPath)
		converted++
	}

	log.Printf("startup ts conversion finished: %d/%d converted", converted, len(tsFiles))
}

type RoomStatus struct {
	RoomBaseName    string        `json:"room_base_name"`
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

func (r *Recorder) loadRooms() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := os.MkdirAll(r.downloadRoot, 0o755); err != nil {
		log.Printf("create download root failed: %v", err)
		return
	}

	b, err := os.ReadFile(r.roomsFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		log.Printf("read rooms file failed: %v", err)
		return
	}

	var urls []string
	if err := json.Unmarshal(b, &urls); err != nil {
		log.Printf("parse rooms file failed: %v", err)
		return
	}

	loaded := 0
	for _, raw := range urls {
		u := strings.TrimSpace(raw)
		if u == "" {
			continue
		}
		if _, exists := r.rooms[u]; exists {
			continue
		}
		r.rooms[u] = &roomRecorder{
			url:   u,
			state: StateIdle,
		}
		loaded++
	}
	if loaded > 0 {
		log.Printf("loaded %d rooms from %s", loaded, r.roomsFile)
	}
}

func (r *Recorder) saveRoomsLocked() error {
	urls := make([]string, 0, len(r.rooms))
	for u := range r.rooms {
		urls = append(urls, u)
	}
	sort.Strings(urls)

	b, err := json.MarshalIndent(urls, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal rooms: %w", err)
	}
	b = append(b, '\n')

	if err := os.MkdirAll(r.downloadRoot, 0o755); err != nil {
		return fmt.Errorf("create download root: %w", err)
	}

	tmp := r.roomsFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("write rooms temp file: %w", err)
	}
	if err := os.Rename(tmp, r.roomsFile); err != nil {
		return fmt.Errorf("rename rooms file: %w", err)
	}
	return nil
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
			RoomBaseName:    roomBaseName(room.url),
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
		url:   m3u8URL,
		state: StateIdle,
	}
	if err := r.saveRoomsLocked(); err != nil {
		delete(r.rooms, m3u8URL)
		return err
	}
	return nil
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

	go r.runRoom(room.runCtx, room.url, room.sessionDir)
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

func (r *Recorder) runRoom(ctx context.Context, m3u8URL, sessionDir string) {
	for {
		if ctx.Err() != nil {
			r.setIdle(m3u8URL)
			return
		}

		filePrefix := time.Now().Format("20060102_150405") + "_"
		r.mu.Lock()
		if room, ok := r.rooms[m3u8URL]; ok {
			room.filePrefix = filePrefix
		}
		r.mu.Unlock()

		cycleCtx, cancelCycle := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func(prefix string) {
			done <- r.runFFmpegOnce(cycleCtx, m3u8URL, sessionDir, prefix)
		}(filePrefix)
		go r.monitorStats(cycleCtx, m3u8URL, sessionDir, filePrefix)

		restartTimer := time.NewTimer(r.splitEvery)
		restartByTimer := false

		select {
		case <-ctx.Done():
			cancelCycle()
			err := <-done
			restartTimer.Stop()
			if err != nil {
				r.setError(m3u8URL, err)
				return
			}
			r.setIdle(m3u8URL)
			return
		case err := <-done:
			restartTimer.Stop()
			cancelCycle()
			if err != nil {
				r.setError(m3u8URL, err)
				return
			}
			log.Printf("[room=%s] ffmpeg exited normally, stop recording", m3u8URL)
			r.setIdle(m3u8URL)
			return
		case <-restartTimer.C:
			restartByTimer = true
			cancelCycle()
			err := <-done
			if err != nil {
				r.setError(m3u8URL, err)
				return
			}
		}

		if restartByTimer {
			log.Printf("[room=%s] restart interval reached (%s), restarting ffmpeg", m3u8URL, r.splitEvery)
		}
	}
}

func (r *Recorder) runFFmpegOnce(ctx context.Context, m3u8URL, sessionDir, filePrefix string) error {
	segmentPattern := filepath.Join(sessionDir, filePrefix+"%06d.ts")
	args := []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-fflags", "+discardcorrupt",
		"-rw_timeout", "15000000",
		"-reconnect", "1",
		"-reconnect_streamed", "1",
		"-reconnect_delay_max", "10",
		"-http_persistent", "0",
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
		return fmt.Errorf("ffmpeg stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	log.Printf("[room=%s] ffmpeg started pid=%d output=%s pattern=%s", m3u8URL, cmd.Process.Pid, sessionDir, filepath.Base(segmentPattern))

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
	if errors.Is(ctx.Err(), context.Canceled) {
		r.convertReadyTS(ctx, m3u8URL, sessionDir, filePrefix, false)
		return nil
	}
	if scanner.Err() != nil {
		return fmt.Errorf("read ffmpeg stderr: %w", scanner.Err())
	}
	if waitErr != nil {
		if len(lastErrLines) > 0 {
			return fmt.Errorf("ffmpeg exited: %v; last logs: %s", waitErr, strings.Join(lastErrLines, " | "))
		}
		return fmt.Errorf("ffmpeg exited: %w", waitErr)
	}

	r.convertReadyTS(ctx, m3u8URL, sessionDir, filePrefix, false)
	return nil
}

func (r *Recorder) monitorStats(ctx context.Context, m3u8URL, dir, prefix string) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			segCount, totalBytes := scanDirStats(dir, prefix)
			r.setStats(m3u8URL, segCount, totalBytes)
			r.convertReadyTS(ctx, m3u8URL, dir, prefix, true)
		}
	}
}

func (r *Recorder) convertReadyTS(ctx context.Context, m3u8URL, dir, prefix string, skipNewest bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	tsFiles := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(strings.ToLower(name), ".ts") {
			continue
		}
		tsFiles = append(tsFiles, filepath.Join(dir, name))
	}
	sort.Strings(tsFiles)
	if skipNewest && len(tsFiles) > 0 {
		tsFiles = tsFiles[:len(tsFiles)-1]
	}

	for _, tsPath := range tsFiles {
		mp4Path := strings.TrimSuffix(tsPath, filepath.Ext(tsPath)) + ".mp4"
		if _, err := os.Stat(mp4Path); err == nil {
			continue
		}
		if err := remuxTS2MP4(ctx, tsPath, mp4Path); err != nil {
			log.Printf("[room=%s] convert %s -> %s failed: %v", m3u8URL, filepath.Base(tsPath), filepath.Base(mp4Path), err)
			continue
		}
		_ = os.Remove(tsPath)
		log.Printf("[room=%s] converted %s -> %s", m3u8URL, filepath.Base(tsPath), filepath.Base(mp4Path))
	}
}

func remuxTS2MP4(ctx context.Context, tsPath, mp4Path string) error {
	tmpOut := mp4Path
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-nostdin",
		"-hide_banner",
		"-loglevel", "error",
		"-y",
		"-i", tsPath,
		"-c", "copy",
		"-bsf:a", "aac_adtstoasc",
		tmpOut,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.Remove(tmpOut)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("ffmpeg remux failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	if err := os.Rename(tmpOut, mp4Path); err != nil {
		_ = os.Remove(tmpOut)
		return fmt.Errorf("rename output: %w", err)
	}
	return nil
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
	if err != nil {
		return "room_" + shortHash(rawURL)
	}
	q := u.Query()
	id := strings.TrimSpace(q.Get("id"))
	title := strings.TrimSpace(q.Get("title"))
	if id != "" && title != "" {
		return sanitizeName(id + "_" + title)
	}
	// 如果只有 id
	if id != "" {
		return sanitizeName(id)
	}
	// fallback 原有逻辑
	b := strings.TrimSpace(path.Base(strings.TrimSuffix(u.Path, "/")))
	if b != "" && b != "." && b != "/" {
		return sanitizeName(b)
	}
	if host := sanitizeName(u.Hostname()); host != "" {
		return host
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
