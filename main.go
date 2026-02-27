package main

import (
	"log"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	DownloadPath string
	SplitSeconds int
	WebPort      string
	RequestUA    string
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

	return Config{
		DownloadPath: dp,
		SplitSeconds: split,
		WebPort:      port,
		RequestUA:    ua,
	}
}

func main() {
	cfg := mustLoadConfig()
	log.Printf("Config: DOWNLOAD_PATH=%s SPLIT_SECONDS=%d WEBUI_PORT=%s REQUEST_UA=%s",
		cfg.DownloadPath, cfg.SplitSeconds, cfg.WebPort, cfg.RequestUA)

	rec := NewRecorder(cfg.DownloadPath, cfg.SplitSeconds, cfg.RequestUA)
	server := NewWebUI(rec)

	log.Fatal(server.Listen(":" + cfg.WebPort))
}
