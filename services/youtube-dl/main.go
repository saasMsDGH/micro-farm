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

// Gestion des ressources et de la congestion
var semaphore = make(chan struct{}, maxConcurrent)
var queueControl = make(chan struct{}, maxConcurrent+maxQueue)

// Identité de navigateur pour le bypass de restriction
const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// transportWithUserAgent injecte le User-Agent dans CHAQUE sous-requête (indispensable pour YouTube)
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
		logger.Warn("FFMPEG non détecté, le muxing HD sera indisponible", "error", err)
	}

	mux := http.NewServeMux()
	contentStatic, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(contentStatic)))

	// API Endpoints
	mux.HandleFunc("/api/stream", queueMiddleware(streamHandler))
	mux.HandleFunc("/metrics", metricsHandler)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	// Middleware de logging pour le monitoring Loki
	loggingHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		mux.ServeHTTP(w, r)
		if r.URL.Path != "/health" {
			logger.Info("Requête traitée", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr, "duration", time.Since(start))
		}
	})

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      loggingHandler,
		ReadTimeout:  30 * time.Minute, // Support pour les très longues vidéos
		WriteTimeout: 30 * time.Minute,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		logger.Info("Démarrage YouTube-DL DGSynthex", "port", port, "version", "0.1.9")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("Erreur fatale serveur", "error", err)
		}
	}()

	<-stop
	logger.Info("Arrêt gracieux en cours...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	server.Shutdown(ctx)
}

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
		if strings.HasPrefix(domain, ".") {
			domain = domain[1:]
		}
		expiresInt, _ := strconv.ParseInt(parts[4], 10, 64)
		cookie := &http.Cookie{
			Name: parts[5], Value: parts[6], Domain: domain,
			Path: parts[2], Secure: parts[3] == "TRUE",
		}
		if expiresInt > 0 {
			cookie.Expires = time.Unix(expiresInt, 0)
		}
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
		http.Error(w, "ID Vidéo invalide", http.StatusBadRequest)
		return
	}

	// Initialisation du client avec Transport et Jar de cookies
	httpClient := &http.Client{
		Timeout:   60 * time.Second,
		Transport: &transportWithUserAgent{base: http.DefaultTransport},
	}

	cookiePath := "/etc/youtube-dl/cookies.txt"
	if _, err := os.Stat(cookiePath); err == nil {
		if f, err := os.Open(cookiePath); err == nil {
			defer f.Close()
			jar := decodeCookies(f)
			httpClient.Jar = jar

			uYT, _ := url.Parse("https://www.youtube.com")
			logger.Info("Vérification session", "youtube_cookies", len(jar.Cookies(uYT)), "id", videoID)
		}
	}

	client := youtube.Client{HTTPClient: httpClient}
	video, err := client.GetVideo(videoID)
	if err != nil {
		logger.Error("YouTube API Error", "id", videoID, "err", err)
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, "Restriction YouTube : %v", err)
		return
	}

	vFormats := video.Formats.Type("video/mp4")
	sort.Slice(vFormats, func(i, j int) bool { return vFormats[i].Height > vFormats[j].Height })

	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q.mp4", video.Title))

	// Choix du mode de streaming
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
		// Mode HD Muxing avec FFmpeg
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

		// Commande FFmpeg optimisée pour le streaming pipe
		cmd := exec.Command("ffmpeg",
			"-i", "pipe:0", // Video sur Stdin
			"-i", "pipe:3", // Audio sur ExtraFiles[0]
			"-c:v", "copy", "-c:a", "aac",
			"-f", "mp4",
			"-movflags", "frag_keyframe+empty_moov+default_base_moof",
			"pipe:1", // Output sur Stdout
		)

		cmd.Stdin = vStream
		cmd.ExtraFiles = []*os.File{streamToFile(aStream)}
		cmd.Stdout = w

		if err := cmd.Run(); err != nil {
			logger.Error("Erreur FFmpeg", "err", err)
		}
	}
}

// queueMiddleware gère la limitation du nombre de téléchargements concurrents
func queueMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		select {
		case queueControl <- struct{}{}:
			defer func() { <-queueControl }()
		default:
			http.Error(w, "File d'attente pleine (SaaS Rate Limit)", http.StatusServiceUnavailable)
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

// streamToFile transforme un ReadCloser en fichier pour FFmpeg pipe
func streamToFile(r io.ReadCloser) *os.File {
	pr, pw, _ := os.Pipe()
	go func() {
		defer pw.Close()
		io.Copy(pw, r)
		r.Close()
	}()
	return pr
}
