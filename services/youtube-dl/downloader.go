package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

var videoIDRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{11}$`)

type streamCacheEntry struct {
	url     string
	expires time.Time
}

var streamURLCache sync.Map // key=videoID|q -> streamCacheEntry

// Backward-compatible : même signature qu’avant.
func GetStreamURL(videoID string) (string, error) {
	return GetStreamURLWithQuality(context.Background(), videoID, "")
}

// Extrait une URL de flux "direct" (progressive) via yt-dlp.
// quality est best-effort ("720", "1080", ...). Pour rester streamable (1 URL),
// on reste sur une sélection progressive MP4 et on retombe sur best[ext=mp4] si besoin.
func GetStreamURLWithQuality(ctx context.Context, videoID, quality string) (string, error) {
	if !videoIDRe.MatchString(videoID) {
		return "", fmt.Errorf("invalid video id")
	}

	cacheKey := videoID + "|" + quality
	if v, ok := streamURLCache.Load(cacheKey); ok {
		ent := v.(streamCacheEntry)
		if time.Now().Before(ent.expires) && ent.url != "" {
			return ent.url, nil
		}
		streamURLCache.Delete(cacheKey)
	}

	target := fmt.Sprintf("https://www.youtube.com/watch?v=%s", videoID)

	// Sélection progressive MP4 (une seule URL).
	// Note : 1080p est souvent DASH (video+audio séparés) => pas muxable en "1 URL".
	format := "best[ext=mp4]"
	switch quality {
	case "720":
		format = "best[ext=mp4][height<=720]/best[ext=mp4]"
	case "1080":
		format = "best[ext=mp4][height<=1080]/best[ext=mp4]"
	}

	// Empêche yt-dlp de bloquer indéfiniment.
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	args := []string{"-g", "-f", format, target}

	// Optionnel : cookies (useful consent/age-restricted).
	if cookiePath := strings.TrimSpace(os.Getenv("YTDLP_COOKIES")); cookiePath != "" {
		args = append([]string{"--cookies", cookiePath}, args...)
	}

	cmd := exec.CommandContext(cctx, "yt-dlp", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("yt-dlp error: %s", msg)
	}

	// yt-dlp peut renvoyer plusieurs lignes (DASH). On prend la première non vide
	// pour éviter de renvoyer une string contenant '\n' (qui cassait la requête HTTP).
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var streamURL string
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln != "" {
			streamURL = ln
			break
		}
	}
	if streamURL == "" {
		return "", fmt.Errorf("yt-dlp returned empty url")
	}

	// Cache très court : les URLs signées expirent vite.
	streamURLCache.Store(cacheKey, streamCacheEntry{
		url:     streamURL,
		expires: time.Now().Add(2 * time.Minute),
	})

	return streamURL, nil
}
