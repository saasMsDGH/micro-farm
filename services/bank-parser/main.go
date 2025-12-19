package main

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/aclindsa/ofxgo"
)

func main() {
	http.HandleFunc("/api/parse", parseHandler)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	http.ListenAndServe(":"+port, nil)
}

func parseHandler(w http.ResponseWriter, r *http.Request) {
	// Limite upload 5MB
	r.Body = http.MaxBytesReader(w, r.Body, 5<<20)

	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Parsing (librairie ofxgo)
	response, err := ofxgo.ParseResponse(file)
	if err != nil {
		http.Error(w, "Parse error: "+err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
