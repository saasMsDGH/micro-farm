package main

import (
	"context"
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// AppLogger est renommé pour éviter les conflits avec d'autres libs éventuelles.
var AppLogger = slog.New(slog.NewJSONHandler(os.Stdout, nil))

//go:embed web/*
var webFS embed.FS

func main() {
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "8080"
	}

	// Comportement inchangé : on attend le VPN par défaut.
	// Dev : REQUIRE_VPN=false pour bypass.
	requireVPN := strings.TrimSpace(os.Getenv("REQUIRE_VPN"))
	if requireVPN == "" || strings.EqualFold(requireVPN, "true") || requireVPN == "1" || strings.EqualFold(requireVPN, "yes") {
		WaitForVPN()
	}

	mux := http.NewServeMux()

	contentStatic, err := fs.Sub(webFS, "web")
	if err != nil {
		AppLogger.Error("Erreur fatale : impossible de lire les assets embed", "err", err)
		os.Exit(1)
	}

	mux.Handle("/", http.FileServer(http.FS(contentStatic)))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/api/stream", StreamHandler)

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           LoggingMiddleware(mux),
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// Pas de WriteTimeout : on stream des gros fichiers.
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		AppLogger.Info("Serveur DGSynthex démarré", "port", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			AppLogger.Error("Erreur fatale", "err", err)
		}
	}()

	<-stop
	AppLogger.Info("Arrêt en cours...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}
