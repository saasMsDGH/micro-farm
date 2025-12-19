# ==============================================================================
# 🚜 GO MICRO FARM - ORCHESTRATOR
# ==============================================================================

PROJECT_NAME := micro-farm
REPO_VJ := github.com/spadmdck/$(PROJECT_NAME)
SERVICES_DIR := services
DOCKER_USER := spadmdck

# On force l'utilisation de bash
SHELL := /bin/bash

# --- CONFIGURATION PATH GO ---
# Nécessaire car $GOPATH/bin n'est pas toujours dans le $PATH système
GOPATH := $(shell go env GOPATH)
ifeq ($(GOPATH),)
	GOPATH := $(HOME)/go
endif
AIR_BIN := $(GOPATH)/bin/air

# Récupère la liste dynamique des dossiers dans services/
SERVICES := $(shell ls $(SERVICES_DIR))

.PHONY: help init-all tidy-all create-service dev docker install-tools kill-port

help:
	@echo "Usage:"
	@echo "  make install-tools         Installe Air (Hot Reload)"
	@echo "  make init-all              Initialize go.mod pour tous les services existants"
	@echo "  make tidy-all              Lance 'go mod tidy' sur tous les services"
	@echo "  make create-service name=x Crée un nouveau microservice"
	@echo "  make dev service=x         Lance un service avec Hot Reload"
	@echo "  make docker service=x      Construit l'image Docker d'un service"

# ==============================================================================
# 0. OUTILS
# ==============================================================================
install-tools:
	@echo "🛠️  Installation de Air..."
	@go install github.com/air-verse/air@latest
	@echo "✅ Air installé dans $(AIR_BIN)"

# ==============================================================================
# 1. INITIALISATION DE MASSE
# ==============================================================================
init-all:
	@echo "🚀 Initialisation de tous les modules Go..."
	@for service in $(SERVICES); do \
		echo "⚙️  Traitement de $$service..."; \
		if [ ! -f "$(SERVICES_DIR)/$$service/go.mod" ]; then \
			echo "   📦 Création du go.mod..."; \
			(cd $(SERVICES_DIR)/$$service && go mod init $(REPO_VJ)/$(SERVICES_DIR)/$$service); \
		else \
			echo "   ✅ go.mod existe déjà."; \
		fi; \
		echo "   🧹 Tidy..."; \
		(cd $(SERVICES_DIR)/$$service && go mod tidy); \
	done
	@echo "✨ Tout est prêt !"

tidy-all:
	@echo "🧹 Nettoyage des dépendances (tidy) partout..."
	@for service in $(SERVICES); do \
		echo "   -> $$service"; \
		(cd $(SERVICES_DIR)/$$service && go mod tidy); \
	done

# ==============================================================================
# 2. GENERATEUR DE SERVICE
# ==============================================================================
create-service:
	@if [ -z "$(name)" ]; then echo "❌ Erreur: Précise le nom (ex: make create-service name=pdf-gen)"; exit 1; fi
	@echo "🏗️  Création du service : $(name)..."
	@mkdir -p $(SERVICES_DIR)/$(name)/web
	
	@# 1. Création du go.mod
	@(cd $(SERVICES_DIR)/$(name) && go mod init $(REPO_VJ)/$(SERVICES_DIR)/$(name))
	
	@# 2. Index.html placeholder
	@echo '<h1>Service $(name)</h1>' > $(SERVICES_DIR)/$(name)/web/index.html

	@# 3. Main.go
	@echo 'package main' > $(SERVICES_DIR)/$(name)/main.go
	@echo '' >> $(SERVICES_DIR)/$(name)/main.go
	@echo 'import (' >> $(SERVICES_DIR)/$(name)/main.go
	@echo '	"embed"' >> $(SERVICES_DIR)/$(name)/main.go
	@echo '	"fmt"' >> $(SERVICES_DIR)/$(name)/main.go
	@echo '	"io/fs"' >> $(SERVICES_DIR)/$(name)/main.go
	@echo '	"log"' >> $(SERVICES_DIR)/$(name)/main.go
	@echo '	"net/http"' >> $(SERVICES_DIR)/$(name)/main.go
	@echo '	"os"' >> $(SERVICES_DIR)/$(name)/main.go
	@echo ')' >> $(SERVICES_DIR)/$(name)/main.go
	@echo '' >> $(SERVICES_DIR)/$(name)/main.go
	@echo '//go:embed web/*' >> $(SERVICES_DIR)/$(name)/main.go
	@echo 'var webFS embed.FS' >> $(SERVICES_DIR)/$(name)/main.go
	@echo '' >> $(SERVICES_DIR)/$(name)/main.go
	@echo 'func main() {' >> $(SERVICES_DIR)/$(name)/main.go
	@echo '	port := os.Getenv("PORT")' >> $(SERVICES_DIR)/$(name)/main.go
	@echo '	if port == "" { port = "8080" }' >> $(SERVICES_DIR)/$(name)/main.go
	@echo '	contentStatic, _ := fs.Sub(webFS, "web")' >> $(SERVICES_DIR)/$(name)/main.go
	@echo '	http.Handle("/", http.FileServer(http.FS(contentStatic)))' >> $(SERVICES_DIR)/$(name)/main.go
	@echo '	fmt.Printf("🚀 $(name) listening on :%s\n", port)' >> $(SERVICES_DIR)/$(name)/main.go
	@echo '	if err := http.ListenAndServe(":"+port, nil); err != nil { log.Fatal(err) }' >> $(SERVICES_DIR)/$(name)/main.go
	@echo '}' >> $(SERVICES_DIR)/$(name)/main.go
	
	@echo "✅ Service $(name) créé !"

# ==============================================================================
# 3. DEV & BUILD
# ==============================================================================

kill-port:
	@echo "🔫 Nettoyage du port $(or $(PORT),8080)..."
	@-fuser -k $(or $(PORT),8080)/tcp 2>/dev/null || true

dev:
	@if [ -z "$(service)" ]; then echo "❌ Erreur: Précise le service"; exit 1; fi
	@$(MAKE) kill-port
	@echo "🔥 Lancement de $(service) avec Hot Reload..."
	@if [ ! -f "$(AIR_BIN)" ]; then \
		echo "❌ Air introuvable à $(AIR_BIN). Lance 'make install-tools' d'abord."; \
		exit 1; \
	fi
	@(cd $(SERVICES_DIR)/$(service) && $(AIR_BIN) -c ../../.air.toml)

docker:
	@if [ -z "$(service)" ]; then echo "❌ Erreur: Précise le service"; exit 1; fi
	docker build \
		-t $(DOCKER_USER)/$(service):latest \
		--build-arg SERVICE_NAME=$(service) \
		-f Dockerfile .