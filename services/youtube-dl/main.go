package main

import (
	"context"
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// AppLogger est rennomé pour éviter les conflits avec le package "logger" de GORM
var AppLogger = slog.New(slog.NewJSONHandler(os.Stdout, nil))

//go:embed web/*
var webFS embed.FS

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	mux := http.NewServeMux()

	// On extrait le sous-répertoire "web" de l'embed
	contentStatic, err := fs.Sub(webFS, "web")
	if err != nil {
		// Si ça échoue ici, il faut arrêter le programme tout de suite
		// pour éviter le panic plus tard
		AppLogger.Error("Erreur fatale : impossible de lire les assets embed", "err", err)
		os.Exit(1)
	}

	// StreamHandler sera défini dans handlers.go
	mux.Handle("/", http.FileServer(http.FS(contentStatic)))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/api/stream", StreamHandler)

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      LoggingMiddleware(mux), // Défini dans handlers.go
		ReadTimeout:  30 * time.Minute,
		WriteTimeout: 30 * time.Minute,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	// Attente du VPN définie dans vpn.go
	WaitForVPN()

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
	server.Shutdown(ctx)
}
