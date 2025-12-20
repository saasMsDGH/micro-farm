package main

import (
	"context"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"sort"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/kkdai/youtube/v2"
)

//go:embed web/*
var webFS embed.FS

var (
	ytIDRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]{11}$`)
	logger    = slog.New(slog.NewJSONHandler(os.Stdout, nil))

	activeDownloads int32 = 0
	maxConcurrent         = 5
	maxQueue              = 5
)

var semaphore = make(chan struct{}, maxConcurrent)
var queueControl = make(chan struct{}, maxConcurrent+maxQueue)

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

type transportWithUserAgent struct {
	base http.RoundTripper
}

func (t *transportWithUserAgent) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", userAgent)
	return t.base.RoundTrip(req)
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	if _, err := exec.LookPath("ffmpeg"); err != nil {
		logger.Warn("FFMPEG non détecté, le muxing HD sera indisponible")
	}

	mux := http.NewServeMux()
	contentStatic, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(contentStatic)))

	mux.HandleFunc("/api/stream", queueMiddleware(streamHandler))
	mux.HandleFunc("/metrics", metricsHandler)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	loggingHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		mux.ServeHTTP(w, r)
		if r.URL.Path != "/health" {
			logger.Info("Requête traitée", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start))
		}
	})

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      loggingHandler,
		ReadTimeout:  30 * time.Minute,
		WriteTimeout: 30 * time.Minute,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		logger.Info("Démarrage YouTube-DL DGSynthex", "port", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("Erreur fatale serveur", "error", err)
		}
	}()

	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	server.Shutdown(ctx)
}

func streamHandler(w http.ResponseWriter, r *http.Request) {
	videoID := r.URL.Query().Get("v")
	quality := r.URL.Query().Get("q")

	if !ytIDRegex.MatchString(videoID) {
		http.Error(w, "ID Vidéo invalide", http.StatusBadRequest)
		return
	}

	// Client HTTP sans cookies, mais avec User-Agent
	httpClient := &http.Client{
		Timeout:   60 * time.Second,
		Transport: &transportWithUserAgent{base: http.DefaultTransport},
	}

	client := youtube.Client{HTTPClient: httpClient}
	video, err := client.GetVideo(videoID)
	if err != nil {
		logger.Error("YouTube API Error", "id", videoID, "err", err)
		http.Error(w, "Erreur YouTube : "+err.Error(), http.StatusForbidden)
		return
	}

	vFormats := video.Formats.Type("video/mp4")
	sort.Slice(vFormats, func(i, j int) bool { return vFormats[i].Height > vFormats[j].Height })

	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q.mp4", video.Title))

	if quality == "720" || quality == "" || len(vFormats) == 0 {
		stream, size, err := client.GetStream(video, &vFormats[0])
		if err != nil {
			http.Error(w, "Erreur de flux", http.StatusInternalServerError)
			return
		}
		defer stream.Close()
		w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
		io.Copy(w, stream)
	} else {
		// Logique FFmpeg conservée pour le moment
		aFormats := video.Formats.WithAudioChannels()
		sort.Slice(aFormats, func(i, j int) bool { return aFormats[i].Bitrate > aFormats[j].Bitrate })

		vStream, _, errV := client.GetStream(video, &vFormats[0])
		aStream, _, errA := client.GetStream(video, &aFormats[0])
		if errV != nil || errA != nil {
			http.Error(w, "Impossible de récupérer les flux HD", http.StatusInternalServerError)
			return
		}
		defer vStream.Close()
		defer aStream.Close()

		cmd := exec.Command("ffmpeg",
			"-i", "pipe:0",
			"-i", "pipe:3",
			"-c:v", "copy", "-c:a", "aac",
			"-f", "mp4",
			"-movflags", "frag_keyframe+empty_moov+default_base_moof",
			"pipe:1",
		)
		cmd.Stdin = vStream
		cmd.ExtraFiles = []*os.File{streamToFile(aStream)}
		cmd.Stdout = w
		if err := cmd.Run(); err != nil {
			logger.Error("Erreur FFmpeg", "err", err)
		}
	}
}

func queueMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		select {
		case queueControl <- struct{}{}:
			defer func() { <-queueControl }()
		default:
			http.Error(w, "File d'attente pleine", http.StatusServiceUnavailable)
			return
		}

		select {
		case semaphore <- struct{}{}:
			atomic.AddInt32(&activeDownloads, 1)
			defer func() {
				<-semaphore
				atomic.AddInt32(&activeDownloads, -1)
			}()
			next(w, r)
		case <-time.After(30 * time.Second):
			http.Error(w, "Délai d'attente expiré", http.StatusServiceUnavailable)
		}
	}
}

func metricsHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "active_downloads %d\n", atomic.LoadInt32(&activeDownloads))
}

func streamToFile(r io.ReadCloser) *os.File {
	pr, pw, _ := os.Pipe()
	go func() {
		defer pw.Close()
		io.Copy(pw, r)
		r.Close()
	}()
	return pr
}
