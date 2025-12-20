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

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// headerTransport injecte le User-Agent dans chaque requête vers YouTube
type headerTransport struct {
	Transport http.RoundTripper
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", userAgent)
	return t.Transport.RoundTrip(req)
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	if _, err := exec.LookPath("ffmpeg"); err != nil {
		logger.Warn("FFMPEG binaire non trouvé", "error", err)
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
		logger.Info("Requête traitée", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start))
	})

	server := &http.Server{
		Addr:        ":" + port,
		Handler:     loggingHandler,
		ReadTimeout: 30 * time.Minute,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		logger.Info("Démarrage YouTube-DL DGSynthex", "port", port, "version", "0.1.8")
		server.ListenAndServe()
	}()

	<-stop
	server.Shutdown(context.Background())
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
		rawDomain := domain
		if strings.HasPrefix(domain, ".") {
			domain = domain[1:]
		}
		expiresInt, _ := strconv.ParseInt(parts[4], 10, 64)
		cookie := &http.Cookie{
			Name: parts[5], Value: parts[6], Domain: rawDomain,
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

	client := youtube.Client{}
	httpClient := &http.Client{
		Timeout:   60 * time.Second,
		Transport: &headerTransport{Transport: http.DefaultTransport},
	}

	cookiePath := "/etc/youtube-dl/cookies.txt"
	if _, err := os.Stat(cookiePath); err == nil {
		if f, err := os.Open(cookiePath); err == nil {
			defer f.Close()
			jar := decodeCookies(f)
			httpClient.Jar = jar
			uYT, _ := url.Parse("https://www.youtube.com")
			uG, _ := url.Parse("https://google.com")
			logger.Info("Validation des cookies chargés", "youtube_count", len(jar.Cookies(uYT)), "google_count", len(jar.Cookies(uG)), "video_id", videoID)
		}
	}

	client.HTTPClient = httpClient
	video, err := client.GetVideo(videoID)
	if err != nil {
		logger.Error("YouTube API Error", "id", videoID, "err", err)
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	vFormats := video.Formats.Type("video/mp4")
	sort.Slice(vFormats, func(i, j int) bool { return vFormats[i].Height > vFormats[j].Height })

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q.mp4", video.Title))
	w.Header().Set("Content-Type", "video/mp4")

	if quality == "720" || quality == "" || len(vFormats) == 0 {
		stream, size, _ := client.GetStream(video, &vFormats[0])
		defer stream.Close()
		w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
		io.Copy(w, stream)
	} else {
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
		cmd.Run()
	}
}

func queueMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		select {
		case queueControl <- struct{}{}:
			defer func() { <-queueControl }()
		default:
			http.Error(w, "Serveur saturé", http.StatusServiceUnavailable)
			return
		}
		select {
		case semaphore <- struct{}{}:
			atomic.AddInt32(&activeDownloads, 1)
			defer func() { <-semaphore; atomic.AddInt32(&activeDownloads, -1) }()
			next(w, r)
		case <-time.After(30 * time.Second):
			http.Error(w, "Timeout", http.StatusServiceUnavailable)
		}
	}
}

func metricsHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "active_downloads %d\n", atomic.LoadInt32(&activeDownloads))
}

func streamToFile(r io.ReadCloser) *os.File {
	pr, pw, _ := os.Pipe()
	go func() { defer pw.Close(); io.Copy(pw, r); r.Close() }()
	return pr
}
