package main

import (
	"bufio"
	"context"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/kkdai/youtube/v2"
	"golang.org/x/net/publicsuffix"
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

// User-Agent moderne pour bypasser les détections "Headless"
const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// Vérification FFMPEG pour le muxing HD
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		logger.Warn("FFMPEG non trouvé, le mode HD sera indisponible", "error", err)
	}

	mux := http.NewServeMux()
	contentStatic, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(contentStatic)))
	mux.HandleFunc("/api/stream", queueMiddleware(streamHandler))
	mux.HandleFunc("/metrics", metricsHandler)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	// Logger de requêtes global
	loggingHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		mux.ServeHTTP(w, r)
		logger.Info("Requête traitée", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start))
	})

	server := &http.Server{
		Addr:        ":" + port,
		Handler:     loggingHandler,
		ReadTimeout: 30 * time.Minute, // Timeout long pour les gros flux vidéo
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		logger.Info("Démarrage YouTube-DL DGSynthex", "port", port, "version", "0.1.8")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("Erreur serveur", "error", err)
		}
	}()

	<-stop
	logger.Info("Arrêt du serveur...")
	server.Shutdown(context.Background())
}

// decodeCookies parse le format Netscape.
// Crucial : Il gère les points devant les domaines pour la portée globale des cookies.
func decodeCookies(r io.Reader) *cookiejar.Jar {
	jar, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	scanner := bufio.NewScanner(r)
	count := 0
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 7 {
			continue
		}

		domain := parts[0]
		rawDomain := domain
		if strings.HasPrefix(domain, ".") {
			domain = domain[1:]
		}

		expiresInt, _ := strconv.ParseInt(parts[4], 10, 64)
		cookie := &http.Cookie{
			Name:   parts[5],
			Value:  parts[6],
			Domain: rawDomain,
			Path:   parts[2],
			Secure: parts[3] == "TRUE",
		}
		if expiresInt > 0 {
			cookie.Expires = time.Unix(expiresInt, 0)
		}

		// On injecte le cookie pour https://domain
		u, _ := url.Parse(fmt.Sprintf("https://%s", domain))
		jar.SetCookies(u, []*http.Cookie{cookie})
		count++
	}
	logger.Info("Jar de cookies initialisé", "total_cookies", count)
	return jar
}

func streamHandler(w http.ResponseWriter, r *http.Request) {
	videoID := r.URL.Query().Get("v")
	quality := r.URL.Query().Get("q")

	if !ytIDRegex.MatchString(videoID) {
		http.Error(w, "ID Vidéo YouTube invalide", http.StatusBadRequest)
		return
	}

	client := youtube.Client{}

	// Configuration du transport avec User-Agent
	httpClient := &http.Client{Timeout: 60 * time.Second}

	// Chargement des cookies depuis le Secret Kubernetes
	cookiePath := "/etc/youtube-dl/cookies.txt"
	if _, err := os.Stat(cookiePath); err == nil {
		if f, err := os.Open(cookiePath); err == nil {
			defer f.Close()
			jar := decodeCookies(f)
			httpClient.Jar = jar

			// Log de confirmation pour le debug
			uYT, _ := url.Parse("https://www.youtube.com")
			uG, _ := url.Parse("https://google.com")
			logger.Info("Validation des cookies chargés",
				"youtube_count", len(jar.Cookies(uYT)),
				"google_count", len(jar.Cookies(uG)),
				"video_id", videoID)
		}
	} else {
		logger.Warn("Cookies absents : les vidéos restreintes échoueront", "path", cookiePath)
	}

	client.HTTPClient = httpClient

	video, err := client.GetVideo(videoID)
	if err != nil {
		handleYTError(w, err, videoID)
		return
	}

	// Sélection du format (Muxing ou direct)
	vFormats := video.Formats.Type("video/mp4")
	sort.Slice(vFormats, func(i, j int) bool { return vFormats[i].Height > vFormats[j].Height })

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q.mp4", video.Title))
	w.Header().Set("Content-Type", "video/mp4")

	// Logique de streaming
	if quality == "720" || quality == "" || len(vFormats) == 0 {
		stream, size, err := client.GetStream(video, &vFormats[0])
		if err != nil {
			logger.Error("Erreur Stream", "err", err)
			return
		}
		defer stream.Close()
		w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
		io.Copy(w, stream)
	} else {
		// Mode HD avec FFmpeg
		aFormats := video.Formats.WithAudioChannels()
		sort.Slice(aFormats, func(i, j int) bool { return aFormats[i].Bitrate > aFormats[j].Bitrate })

		vStream, _, _ := client.GetStream(video, &vFormats[0])
		aStream, _, _ := client.GetStream(video, &aFormats[0])
		defer vStream.Close()
		defer aStream.Close()

		cmd := exec.Command("ffmpeg", "-i", "pipe:0", "-i", "pipe:3", "-c:v", "copy", "-c:a", "aac", "-f", "mp4", "-movflags", "frag_keyframe+empty_moov", "pipe:1")
		cmd.Stdin = vStream
		cmd.ExtraFiles = []*os.File{streamToFile(aStream)}
		cmd.Stdout = w
		if err := cmd.Run(); err != nil {
			logger.Error("Erreur FFmpeg", "error", err)
		}
	}
}

// handleYTError traduit les erreurs obscures de YouTube en messages clairs pour ton SaaS
func handleYTError(w http.ResponseWriter, err error, id string) {
	msg := err.Error()
	logger.Error("YouTube API Error", "id", id, "details", msg)

	if strings.Contains(msg, "age restriction") {
		http.Error(w, "Veuillez rafraîchir les cookies (Restriction d'âge détectée)", http.StatusForbidden)
	} else if strings.Contains(msg, "embedding") {
		http.Error(w, "Accès bloqué par YouTube (Embedding disabled). Vérifiez le User-Agent.", http.StatusForbidden)
	} else if strings.Contains(msg, "403") {
		http.Error(w, "IP bannie ou Session expirée (Erreur 403)", http.StatusForbidden)
	} else {
		http.Error(w, "YouTube dit : "+msg, http.StatusBadGateway)
	}
}

func queueMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		select {
		case queueControl <- struct{}{}:
			defer func() { <-queueControl }()
		default:
			http.Error(w, "Serveur de téléchargement saturé", http.StatusServiceUnavailable)
			return
		}
		select {
		case semaphore <- struct{}{}:
			atomic.AddInt32(&activeDownloads, 1)
			defer func() { <-semaphore; atomic.AddInt32(&activeDownloads, -1) }()
			next(w, r)
		case <-time.After(30 * time.Second):
			http.Error(w, "Délai d'attente dépassé", http.StatusServiceUnavailable)
		}
	}
}

func metricsHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "# HELP active_downloads Nombre de flux actifs\n")
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
