package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"
)

func WaitForVPN() {
	const ovhIPPrefix = "151.80"
	AppLogger.Info("Vérification de la connectivité VPN...")

	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://ifconfig.me/ip", nil)

		resp, err := controlHTTPClient.Do(req)
		if err == nil && resp != nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()

			ip := strings.TrimSpace(string(body))
			if ip != "" && !strings.HasPrefix(ip, ovhIPPrefix) {
				cancel()
				AppLogger.Info("VPN Opérationnel", "public_ip", ip)
				return
			}
			if ip != "" {
				AppLogger.Warn("Fuite IP détectée (OVH toujours actif)", "ip", ip)
			}
		} else {
			AppLogger.Error("Impossible de joindre ifconfig.me", "err", err)
		}
		cancel()

		AppLogger.Info("Nouvelle tentative dans 5 secondes...")
		time.Sleep(5 * time.Second)
	}
}
