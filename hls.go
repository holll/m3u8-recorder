package main

import (
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

type Playlist struct {
	BaseURL        *url.URL
	TargetDuration int
	MediaSequence  int64
	Segments       []Segment
}

type Segment struct {
	URL          string
	DurationSec  float64
	Seq          *int64
	DiscoveredAt time.Time
}

func (s Segment) Filename() string {
	ts := time.Now().Format("150405")
	if s.Seq != nil {
		return fmt.Sprintf("%d_%s.ts", *s.Seq, ts)
	}
	// fallback: use last path element
	u, err := url.Parse(s.URL)
	if err == nil {
		b := path.Base(u.Path)
		if b != "" && b != "/" && strings.Contains(b, ".") {
			return b
		}
	}
	return fmt.Sprintf("seg_%s.ts", ts)
}

func ParseM3U8(playlistURL string, body string) (*Playlist, error) {
	base, err := url.Parse(playlistURL)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(body, "\n")
	pl := &Playlist{
		BaseURL: base,
	}
	var lastInf *float64
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-TARGETDURATION:") {
			v := strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:")
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				pl.TargetDuration = n
			}
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:") {
			v := strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:")
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				pl.MediaSequence = n
			}
			continue
		}
		if strings.HasPrefix(line, "#EXTINF:") {
			v := strings.TrimPrefix(line, "#EXTINF:")
			v = strings.TrimSuffix(v, ",")
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				lastInf = &f
			} else {
				lastInf = nil
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}

		// segment uri line
		segURL := resolveURL(base, line)
		seg := Segment{
			URL:          segURL,
			DiscoveredAt: time.Now(),
		}
		if lastInf != nil {
			seg.DurationSec = *lastInf
		}
		// seq：如果有 media-seq，用当前 segments index 推算
		if pl.MediaSequence > 0 {
			seq := pl.MediaSequence + int64(len(pl.Segments))
			seg.Seq = &seq
		}
		pl.Segments = append(pl.Segments, seg)
		lastInf = nil
	}

	if len(pl.Segments) == 0 {
		// 有些 m3u8 是 master playlist（包含多个 variant），这里不做自动选码率
		// 你可以把 variant 的 m3u8 传进来录制
		// 如果要自动选，可扩展：解析非 # 行为子 playlist，再选择带 BANDWIDTH 最大/最小的
	}

	return pl, nil
}

func resolveURL(base *url.URL, ref string) string {
	u, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return base.ResolveReference(u).String()
}
