package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type loggingResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (lw *loggingResponseWriter) WriteHeader(code int) {
	lw.status = code
	lw.ResponseWriter.WriteHeader(code)
}

func (lw *loggingResponseWriter) Write(p []byte) (int, error) {
	if lw.status == 0 {
		lw.status = http.StatusOK
	}
	n, err := lw.ResponseWriter.Write(p)
	lw.bytes += int64(n)
	return n, err
}

// LoggingMiddleware : logs JSON (stdout) pour Loki. /health est exclu.
func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		lw := &loggingResponseWriter{ResponseWriter: w}

		next.ServeHTTP(lw, r)

		AppLogger.Info("http_request",
			"method", r.Method,
			"path", r.URL.Path,
			"query", r.URL.RawQuery,
			"status", lw.status,
			"bytes", lw.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote", r.RemoteAddr,
			"ua", r.UserAgent(),
		)
	})
}

// StreamHandler : résout une URL via yt-dlp, puis proxy le flux en streaming.
func StreamHandler(w http.ResponseWriter, r *http.Request) {
	allowedRef := strings.TrimSpace(os.Getenv("ALLOWED_REFERER_SUBSTRING"))
	if allowedRef == "" {
		allowedRef = "flash.dgsynthex.online"
	}
	if !strings.Contains(r.Referer(), allowedRef) {
		http.Error(w, "Accès interdit", http.StatusForbidden)
		return
	}

	// Protection conservée (pas de régression)
	if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
		http.Error(w, "Requête non autorisée", http.StatusForbidden)
		return
	}

	// Sécurité optionnelle : si API_KEY est définie, on exige la clé.
	// Par défaut vide => comportement inchangé.
	if apiKey := strings.TrimSpace(os.Getenv("API_KEY")); apiKey != "" {
		provided := r.Header.Get("X-Api-Key")
		if provided == "" {
			provided = r.URL.Query().Get("api_key")
		}
		if provided != apiKey {
			http.Error(w, "Clé API invalide", http.StatusForbidden)
			return
		}
	}

	// Streaming long : disable deadline (best-effort)
	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetWriteDeadline(time.Time{})
	}

	videoID := r.URL.Query().Get("v")
	quality := r.URL.Query().Get("q")

	streamURL, err := GetStreamURLWithQuality(r.Context(), videoID, quality)
	if err != nil {
		AppLogger.Error("yt-dlp extraction failed", "video_id", videoID, "err", err)
		http.Error(w, "Erreur d'extraction YouTube", http.StatusBadGateway)
		return
	}

	// Garde-fou : uniquement http(s)
	parsed, err := url.Parse(streamURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		http.Error(w, "URL de flux invalide", http.StatusBadGateway)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, streamURL, nil)
	if err != nil {
		http.Error(w, "Erreur de requête upstream", http.StatusBadGateway)
		return
	}

	// Support reprise / Range
	if rng := r.Header.Get("Range"); rng != "" {
		req.Header.Set("Range", rng)
	}
	if ua := r.UserAgent(); ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	if acc := r.Header.Get("Accept"); acc != "" {
		req.Header.Set("Accept", acc)
	}

	resp, err := streamingHTTPClient.Do(req)
	if err != nil {
		http.Error(w, "Erreur de connexion au flux", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copie headers clés (Range inclus)
	for _, h := range []string{
		"Content-Type",
		"Content-Length",
		"Accept-Ranges",
		"Content-Range",
		"ETag",
		"Last-Modified",
	} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", videoID+".mp4"))
	w.Header().Set("X-Accel-Buffering", "no") // best-effort anti-buffering reverse proxy

	// Important : propage status code réel (200/206/403/...)
	w.WriteHeader(resp.StatusCode)

	buf := make([]byte, 32*1024)
	if _, err := io.CopyBuffer(w, resp.Body, buf); err != nil {
		AppLogger.Error("streaming copy failed", "err", err)
	}
}
