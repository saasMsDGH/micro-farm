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

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// Vérification de la présence de ffmpeg au démarrage
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		logger.Error("FFMPEG binaire non trouvé dans le PATH", "error", err)
	}

	mux := http.NewServeMux()

	// Assets statiques
	contentStatic, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(contentStatic)))

	// API avec gestion d'erreurs détaillée
	mux.HandleFunc("/api/stream", queueMiddleware(streamHandler))
	mux.HandleFunc("/metrics", metricsHandler)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	// Middleware de logging global pour voir les 404 réelles
	loggingHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		logger.Info("Requête reçue",
			"method", r.Method,
			"path", r.URL.Path,
			"remote", r.RemoteAddr,
			"agent", r.UserAgent())
		mux.ServeHTTP(w, r)
		logger.Debug("Requête traitée", "duration", time.Since(start))
	})

	server := &http.Server{
		Addr:        ":" + port,
		Handler:     loggingHandler,
		ReadTimeout: 15 * time.Second,
		IdleTimeout: 120 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		logger.Info("Démarrage YouTube-DL", "port", port, "version", "0.1.6")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("Erreur fatale serveur", "err", err)
			os.Exit(1)
		}
	}()

	<-stop
	logger.Info("Arrêt du pod...")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	server.Shutdown(ctx)
}

func queueMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 1. Admission dans la file d'attente globale du Pod
		select {
		case queueControl <- struct{}{}:
			defer func() { <-queueControl }()
		default:
			http.Error(w, "Serveur saturé (Queue pleine)", http.StatusServiceUnavailable)
			return
		}

		// 2. Attribution d'un slot de téléchargement actif
		select {
		case semaphore <- struct{}{}:
			atomic.AddInt32(&activeDownloads, 1)
			defer func() {
				<-semaphore
				atomic.AddInt32(&activeDownloads, -1)
			}()
			next(w, r)
		case <-time.After(30 * time.Second):
			http.Error(w, "Temps d'attente dépassé", http.StatusServiceUnavailable)
		}
	}
}

func metricsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, "# HELP active_downloads Téléchargements en cours\n")
	fmt.Fprintf(w, "# TYPE active_downloads gauge\n")
	fmt.Fprintf(w, "active_downloads %d\n", atomic.LoadInt32(&activeDownloads))
}

func streamHandler(w http.ResponseWriter, r *http.Request) {
	videoID := r.URL.Query().Get("v")
	quality := r.URL.Query().Get("q")

	if !ytIDRegex.MatchString(videoID) {
		logger.Warn("ID Vidéo refusé par Regex", "id", videoID)
		http.Error(w, "Format d'ID YouTube invalide", http.StatusBadRequest)
		return
	}

	client := youtube.Client{}
	video, err := client.GetVideo(videoID)
	if err != nil {
		logger.Error("Erreur API YouTube", "id", videoID, "error", err.Error())
		http.Error(w, fmt.Sprintf("Erreur YouTube: %v", err), http.StatusNotFound)
		return
	}

	// Filtrage des formats
	vFormats := video.Formats.Type("video/mp4")
	if len(vFormats) == 0 {
		logger.Error("Aucun format vidéo trouvé", "id", videoID)
		http.Error(w, "Vidéo non disponible en MP4", http.StatusInternalServerError)
		return
	}
	sort.Slice(vFormats, func(i, j int) bool { return vFormats[i].Height > vFormats[j].Height })

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q.mp4", video.Title))
	w.Header().Set("Content-Type", "video/mp4")

	if quality == "720" || quality == "" {
		stream, size, err := client.GetStream(video, &vFormats[0])
		if err != nil {
			logger.Error("Erreur initialisation flux", "id", videoID, "err", err)
			http.Error(w, "Erreur de stream", http.StatusInternalServerError)
			return
		}
		defer stream.Close()
		w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
		io.Copy(w, stream)
	} else {
		// Logique Muxing HD avec log spécifique FFmpeg
		aFormats := video.Formats.WithAudioChannels()
		if len(aFormats) == 0 {
			http.Error(w, "Audio non trouvé", http.StatusInternalServerError)
			return
		}
		sort.Slice(aFormats, func(i, j int) bool { return aFormats[i].Bitrate > aFormats[j].Bitrate })

		vStream, _, _ := client.GetStream(video, &vFormats[0])
		aStream, _, _ := client.GetStream(video, &aFormats[0])
		defer vStream.Close()
		defer aStream.Close()

		logger.Info("Démarrage du Muxing FFmpeg", "id", videoID, "quality", quality)
		cmd := exec.Command("ffmpeg", "-i", "pipe:0", "-i", "pipe:3", "-c:v", "copy", "-c:a", "aac", "-f", "mp4", "-movflags", "frag_keyframe+empty_moov", "pipe:1")
		cmd.Stdin = vStream
		cmd.ExtraFiles = []*os.File{streamToFile(aStream)}
		cmd.Stdout = w

		if err := cmd.Run(); err != nil {
			logger.Error("Échec critique FFmpeg", "id", videoID, "error", err)
		}
	}
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
