package main

import (
	"fmt"
	"io"
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
	rc := http.NewResponseController(w)

	err := rc.SetWriteDeadline(time.Time{}) // time.Time{} = zéro (infini)
	if err != nil {
		AppLogger.Error("Impossible de modifier le deadline", "err", err)
	}

	videoID := r.URL.Query().Get("v")
	if videoID == "" {
		http.Error(w, "ID Manquant", 400)
		return
	}

	// 1. On récupère l'URL du flux via yt-dlp
	streamURL, err := GetStreamURL(videoID)
	if err != nil {
		AppLogger.Error("Erreur extraction", "id", videoID, "err", err)
		http.Error(w, "Erreur YouTube", 500)
		return
	}

	// 2. On crée une requête vers YouTube depuis le serveur
	resp, err := http.Get(streamURL)
	if err != nil {
		http.Error(w, "Erreur de connexion au flux", 502)
		return
	}
	defer resp.Body.Close()

	// 3. On recopie les headers importants pour le navigateur
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Content-Length", resp.Header.Get("Content-Length"))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s.mp4\"", videoID))

	// 4. On "Pipe" le flux en temps réel (Streaming)
	// io.Copy ne consomme pas de RAM, il transfère les paquets au fur et à mesure
	_, err = io.Copy(w, resp.Body)
	if err != nil {
		AppLogger.Error("Erreur pendant le streaming", "err", err)
	}
}
