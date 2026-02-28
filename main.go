package main

import (
	"errors"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	DownloadPath string
	SplitSeconds int
	WebPort      string
	RequestUA    string
	ScheduleOn   bool
	ScheduleFrom int
	ScheduleTo   int
}

func parseHHMMToMinutes(v string) (int, error) {
	v = strings.TrimSpace(v)
	if len(v) != 5 || v[2] != ':' {
		return 0, errors.New("invalid time format, want HH:MM")
	}
	hh, err := strconv.Atoi(v[:2])
	if err != nil {
		return 0, err
	}
	mm, err := strconv.Atoi(v[3:])
	if err != nil {
		return 0, err
	}
	if hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, errors.New("time out of range")
	}
	return hh*60 + mm, nil
}

func mustLoadConfig() Config {
	_ = godotenv.Load() // 没有也不致命，允许用系统环境变量

	dp := os.Getenv("DOWNLOAD_PATH")
	if dp == "" {
		dp = "./downloads"
	}

	splitStr := os.Getenv("SPLIT_SECONDS")
	split := 600
	if splitStr != "" {
		if v, err := strconv.Atoi(splitStr); err == nil && v > 0 {
			split = v
		}
	}

	port := os.Getenv("WEBUI_PORT")
	if port == "" {
		port = "8080"
	}

	ua := os.Getenv("REQUEST_UA")
	if ua == "" {
		ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
	}

	scheduleStartStr := strings.TrimSpace(os.Getenv("RECORD_SCHEDULE_START"))
	scheduleEndStr := strings.TrimSpace(os.Getenv("RECORD_SCHEDULE_END"))
	scheduleOn := scheduleStartStr != "" && scheduleEndStr != ""
	scheduleStart := 0
	scheduleEnd := 0
	if scheduleOn {
		v, err := parseHHMMToMinutes(scheduleStartStr)
		if err != nil {
			log.Fatalf("invalid RECORD_SCHEDULE_START: %v", err)
		}
		scheduleStart = v
		v, err = parseHHMMToMinutes(scheduleEndStr)
		if err != nil {
			log.Fatalf("invalid RECORD_SCHEDULE_END: %v", err)
		}
		scheduleEnd = v
	}

	return Config{
		DownloadPath: dp,
		SplitSeconds: split,
		WebPort:      port,
		RequestUA:    ua,
		ScheduleOn:   scheduleOn,
		ScheduleFrom: scheduleStart,
		ScheduleTo:   scheduleEnd,
	}
}

func main() {
	cfg := mustLoadConfig()
	log.Printf("Config: DOWNLOAD_PATH=%s SPLIT_SECONDS=%d WEBUI_PORT=%s REQUEST_UA=%s SCHEDULE_ON=%v SCHEDULE=%s-%s",
		cfg.DownloadPath, cfg.SplitSeconds, cfg.WebPort, cfg.RequestUA, cfg.ScheduleOn, minutesToHHMM(cfg.ScheduleFrom), minutesToHHMM(cfg.ScheduleTo))

	rec := NewRecorder(cfg.DownloadPath, cfg.SplitSeconds, cfg.RequestUA, cfg.ScheduleFrom, cfg.ScheduleTo, cfg.ScheduleOn)
	server := NewWebUI(rec)

	log.Fatal(server.Listen(":" + cfg.WebPort))
}
