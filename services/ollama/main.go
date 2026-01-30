package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

type OllamaRequest struct {
	Model  string   `json:"model"`
	Prompt string   `json:"prompt"`
	Images []string `json:"images"` // Base64 de ton ticket de caisse
	Stream bool     `json:"stream"`
}

func main() {
	// URL du service interne k3s
	url := "http://ollama-service.ocr-system.svc.cluster.local:11434/api/generate"

	// Ton prompt spécialisé pour l'OCR structuré
	prompt := "Analyse ce ticket de caisse. Extrais le marchand, la date, le montant total TTC et la TVA. Réponds uniquement en JSON."

	// Prépare ta requête (ajoute ton image en base64 dans Images)
	reqBody, _ := json.Marshal(OllamaRequest{
		Model:  "llama3.2-vision",
		Prompt: prompt,
		Images: []string{"<BASE64_DE_TON_IMAGE>"},
		Stream: false,
	})

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		fmt.Printf("Erreur d'appel Ollama: %v\n", err)
		return
	}
	defer resp.Body.Close()

	// Traite la réponse...
	fmt.Println("Analyse terminée.")
}
