package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// AppLogger est rennomé pour éviter les conflits avec le package "logger" de GORM
var AppLogger = slog.New(slog.NewJSONHandler(os.Stdout, nil))

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	mux := http.NewServeMux()

	// StreamHandler sera défini dans handlers.go
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	// Attente du VPN définie dans vpn.go
	WaitForVPN()
	mux.HandleFunc("/api/stream", StreamHandler)

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      LoggingMiddleware(mux), // Défini dans handlers.go
		ReadTimeout:  30 * time.Minute,
		WriteTimeout: 30 * time.Minute,
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
	server.Shutdown(ctx)
}
