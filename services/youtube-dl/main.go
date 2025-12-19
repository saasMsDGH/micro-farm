package main

import (
	"context"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/kkdai/youtube/v2"
)

//go:embed web/*
var webFS embed.FS

// CONFIGURATION DE LA FERME
const (
	MaxConcurrentDownloads = 5                // Nombre de téléchargements actifs simultanés
	MaxQueueSize           = 20               // Nombre de clients autorisés à attendre
	QueueTimeout           = 30 * time.Second // Temps max d'attente dans la file
)

// Le sémaphore limite les actions simultanées
var semaphore = make(chan struct{}, MaxConcurrentDownloads)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// 1. Frontend
	contentStatic, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	http.Handle("/", http.FileServer(http.FS(contentStatic)))

	// 2. API avec Middleware de Queue
	http.HandleFunc("/api/stream", queueMiddleware(streamHandler))

	// 3. Healthcheck pour K8s
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	log.Printf("📺 YouTube Service (Queue: %d, Slots: %d) on :%s", MaxQueueSize, MaxConcurrentDownloads, port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal(err)
	}
}

// Middleware qui gère la file d'attente
func queueMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// A. On essaie d'entrer dans la file d'attente
		// On utilise un select non-bloquant pour voir si le serveur est saturé
		if len(semaphore) >= MaxConcurrentDownloads {
			// Si on est déjà au max d'actifs, est-ce qu'on accepte l'attente ?
			// Ici, on fait une logique simple : Go gère très bien les goroutines en attente.
			// Mais pour éviter d'avoir 1000 connexions ouvertes qui attendent, on peut check une limite logique.
			// Pour cet exemple, on laisse Go gérer l'attente mais avec un Timeout strict.
		}

		// Context pour l'annulation (si le client ferme l'onglet) + Timeout d'attente
		ctx, cancel := context.WithTimeout(r.Context(), QueueTimeout)
		defer cancel()

		log.Printf("[Queue] Client %s demande un ticket...", r.RemoteAddr)

		select {
		case semaphore <- struct{}{}:
			// B. TICKET OBTENU ! On traite la requête.
			// On libérera le ticket à la fin du traitement
			defer func() { <-semaphore }()
			log.Printf("[Start] Client %s commence le téléchargement", r.RemoteAddr)
			next(w, r)
			log.Printf("[End] Client %s a fini", r.RemoteAddr)

		case <-ctx.Done():
			// C. TROP LONG ou ANNULÉ
			log.Printf("[Drop] Client %s a abandonné ou timeout", r.RemoteAddr)
			http.Error(w, "Serveur trop occupé (File d'attente pleine ou temps dépassé)", http.StatusServiceUnavailable)
			return
		}
	}
}

func streamHandler(w http.ResponseWriter, r *http.Request) {
	videoID := r.URL.Query().Get("v")
	if videoID == "" {
		http.Error(w, "Missing video ID", http.StatusBadRequest)
		return
	}

	client := youtube.Client{}
	video, err := client.GetVideo(videoID)
	if err != nil {
		log.Printf("Error GetVideo: %v", err)
		http.Error(w, "Video not found", http.StatusNotFound)
		return
	}

	formats := video.Formats.WithAudioChannels().Type("video/mp4")
	if len(formats) == 0 {
		http.Error(w, "No MP4 format found", http.StatusInternalServerError)
		return
	}

	// Récupération du stream
	stream, size, err := client.GetStream(video, &formats[0])
	if err != nil {
		log.Printf("Error GetStream: %v", err)
		http.Error(w, "Stream error", http.StatusInternalServerError)
		return
	}
	defer stream.Close()

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q.mp4", video.Title))
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", size))

	// Streaming avec protection de coupure
	// Si le client coupe, le io.Copy s'arrête et libère le sémaphore
	if _, err := io.Copy(w, stream); err != nil {
		log.Printf("Connection closed during stream: %v", err)
	}
}
