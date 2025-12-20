package main

import (
	"net/http"
	"time"
)

// LoggingMiddleware logue chaque requête vers ton monitoring (Loki)
func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if r.URL.Path != "/health" {
			AppLogger.Info("Requête traitée",
				"method", r.Method,
				"path", r.URL.Path,
				"duration", time.Since(start),
			)
		}
	})
}

// StreamHandler gère l'appel à yt-dlp
func StreamHandler(w http.ResponseWriter, r *http.Request) {
	videoID := r.URL.Query().Get("v")
	if videoID == "" {
		http.Error(w, "ID Vidéo manquant", http.StatusBadRequest)
		return
	}

	// Utilisation de la fonction définie dans downloader.go
	streamURL, err := GetStreamURL(videoID)
	if err != nil {
		AppLogger.Error("Erreur yt-dlp", "id", videoID, "err", err)
		http.Error(w, "Erreur de récupération du flux", http.StatusInternalServerError)
		return
	}

	// Pour l'instant, on redirige vers l'URL directe (ou on peut faire un proxy io.Copy)
	http.Redirect(w, r, streamURL, http.StatusTemporaryRedirect)
}
