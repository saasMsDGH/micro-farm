package main

import (
	"fmt"
	"os/exec"
	"strings"
)

func GetStreamURL(videoID string) (string, error) {
	url := fmt.Sprintf("https://www.youtube.com/watch?v=%s", videoID)
	// Appel à yt-dlp (installé dans l'image Alpine)
	// -g : renvoie uniquement l'URL du flux
	cmd := exec.Command("yt-dlp", "-g", "-f", "best[ext=mp4]", url)

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("yt-dlp error: %v", err)
	}

	return strings.TrimSpace(string(out)), nil
}
