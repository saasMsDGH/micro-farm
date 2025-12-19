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

	// Paramètres de pilotage et métriques
	activeDownloads int32 = 0
	maxConcurrent   = 5
	maxQueue        = 5
)

// Gestion des flux et de la file d'attente
var semaphore = make(chan struct{}, maxConcurrent)
var queueControl = make(chan struct{}, maxConcurrent+maxQueue)

func main() {
	port := os.Getenv("PORT")
	if port == "" { port = "8080" }

	mux := http.NewServeMux()
	contentStatic, _ := fs.Sub(webFS, "web")
	
	mux.Handle("/", http.FileServer(http.FS(contentStatic)))
	mux.HandleFunc("/api/stream", queueMiddleware(streamHandler))
	mux.HandleFunc("/metrics", metricsHandler)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		logger.Info("SaaS YouTube-DL démarré", "port", port, "limit", maxConcurrent)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("Erreur serveur", "err", err)
			os.Exit(1)
		}
	}()

	<-stop
	logger.Info("Arrêt propre en cours...")
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
		http.Error(w, "ID Invalide", http.StatusBadRequest)
		return
	}

	client := youtube.Client{}
	video, err := client.GetVideo(videoID)
	if err != nil {
		http.Error(w, "Vidéo introuvable", http.StatusNotFound)
		return
	}

	vFormats := video.Formats.Type("video/mp4")
	sort.Slice(vFormats, func(i, j int) bool { return vFormats[i].Height > vFormats[j].Height })
	
	aFormats := video.Formats.WithAudioChannels()
	sort.Slice(aFormats, func(i, j int) bool { return aFormats[i].Bitrate > aFormats[j].Bitrate })

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q.mp4", video.Title))
	w.Header().Set("Content-Type", "video/mp4")

	if quality == "720" || quality == "" {
		stream, size, _ := client.GetStream(video, &vFormats[0])
		defer stream.Close()
		w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
		io.Copy(w, stream)
	} else {
		// Muxing HD avec FFMPEG statique
		vStream, _, _ := client.GetStream(video, &vFormats[0])
		aStream, _, _ := client.GetStream(video, &aFormats[0])
		defer vStream.Close()
		defer aStream.Close()

		cmd := exec.Command("ffmpeg", "-i", "pipe:0", "-i", "pipe:3", "-c:v", "copy", "-c:a", "aac", "-f", "mp4", "-movflags", "frag_keyframe+empty_moov", "pipe:1")
		cmd.Stdin = vStream
		cmd.ExtraFiles = []*os.File{streamToFile(aStream)}
		cmd.Stdout = w
		cmd.Run()
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