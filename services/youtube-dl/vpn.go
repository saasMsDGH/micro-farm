package main

import (
	"io"
	"net/http"
	"strings"
	"time"
)

func WaitForVPN() {
	const ovhIPPrefix = "151.80"
	AppLogger.Info("Vérification de la connectivité VPN...")

	for {
		resp, err := http.Get("https://ifconfig.me/ip")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			ip := strings.TrimSpace(string(body))
			resp.Body.Close()

			if !strings.HasPrefix(ip, ovhIPPrefix) {
				AppLogger.Info("VPN Opérationnel", "public_ip", ip)
				return
			}
			AppLogger.Warn("Fuite IP détectée (OVH toujours actif)", "ip", ip)
		} else {
			AppLogger.Error("Impossible de joindre ifconfig.me", "err", err)
		}

		AppLogger.Info("Nouvelle tentative dans 5 secondes...")
		time.Sleep(5 * time.Second)
	}
}
