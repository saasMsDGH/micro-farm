# Export de projet

_Généré le 2025-12-19T00:56:21+01:00_

## .air.toml

```toml
root = "."
tmp_dir = "tmp"

[build]
  cmd = "go build -o ./tmp/main ."
  bin = "./tmp/main"
  stop_on_error = true
  include_ext = ["go", "tpl", "tmpl", "html", "css", "js"]
  exclude_dir = ["assets", "tmp", "vendor", "testdata"]
  delay = 1000

[log]
  time = true

[color]
  main = "magenta"
  watcher = "cyan"
  build = "yellow"
  runner = "green"

[misc]
  clean_on_exit = true
```

## .github/workflows/ci-cd.yml

```yaml
name: Smart Build & Publish

on:
  push:
    branches: ["main"]
    paths:
      - 'services/**' # Trigger seulement si un service change
      - 'pkg/**'      # Ou si le code partagé change (dans ce cas, faut tout rebuild, logique à affiner)
      - 'Dockerfile'

jobs:
  detect-changes:
    runs-on: ubuntu-latest
    outputs:
      matrix: ${{ steps.set-matrix.outputs.matrix }}
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 2

      - name: Detect changed services
        id: set-matrix
        run: |
          # Liste les dossiers parents dans services/ qui ont des fichiers modifiés
          CHANGES=$(git diff --name-only HEAD^ HEAD | grep '^services/' | cut -d/ -f2 | sort -u | jq -R . | jq -s -c .)
          
          # Si pkg/ ou Dockerfile change, on rebuild TOUT (sécurité)
          GLOBAL_CHANGE=$(git diff --name-only HEAD^ HEAD | grep -E '^(pkg/|Dockerfile)')
          if [ ! -z "$GLOBAL_CHANGE" ]; then
             echo "⚠️ Global change detected, rebuilding all..."
             CHANGES=$(ls services | jq -R . | jq -s -c .)
          fi

          if [ -z "$CHANGES" ]; then CHANGES="[]"; fi
          echo "matrix=$CHANGES" >> $GITHUB_OUTPUT

  build-and-push:
    needs: detect-changes
    if: needs.detect-changes.outputs.matrix != '[]'
    runs-on: ubuntu-latest
    strategy:
      matrix:
        service: ${{ fromJson(needs.detect-changes.outputs.matrix) }}
    
    steps:
      - uses: actions/checkout@v4

      - name: Login to Docker Hub
        uses: docker/login-action@v3
        with:
          username: ${{ secrets.DOCKER_USERNAME }}
          password: ${{ secrets.DOCKER_PASSWORD }}

      - name: Build and Push ${{ matrix.service }}
        uses: docker/build-push-action@v5
        with:
          context: .
          file: Dockerfile
          push: true
          build-args: |
            SERVICE_NAME=${{ matrix.service }}
          tags: |
            ${{ secrets.DOCKER_USERNAME }}/${{ matrix.service }}:latest
            ${{ secrets.DOCKER_USERNAME }}/${{ matrix.service }}:${{ github.sha }}
```

## Dockerfile

```text
# ==============================================================================
# BASE BUILDER (Cache les outils communs)
# ==============================================================================
FROM golang:1.23-alpine AS builder

# Arguments passés par le build command (ex: --build-arg SERVICE_NAME=youtube-dl)
ARG SERVICE_NAME

WORKDIR /build

# Installation des dépendances système (Certificats, Git)
RUN apk add --no-cache git ca-certificates tzdata

# 1. On prépare le dossier du service spécifique
# On assume que le build context est la racine du monorepo
COPY services/${SERVICE_NAME}/go.mod services/${SERVICE_NAME}/go.sum ./services/${SERVICE_NAME}/
# Si tu as un dossier pkg partagé à la racine
COPY pkg/ ./pkg/

# 2. Download des dépendances (Cache Docker optimisé)
WORKDIR /build/services/${SERVICE_NAME}
RUN go mod download

# 3. Copie du code source complet du service
COPY services/${SERVICE_NAME}/ .

# 4. Compilation Statique (Comme dans ton projet Klaro)
# CGO_ENABLED=0 est vital pour scratch
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o /app/server .

# ==============================================================================
# FINAL IMAGE (Scratch - Ultra léger ~10-20Mo)
# ==============================================================================
FROM scratch

ARG SERVICE_NAME

# Import des fichiers système essentiels
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /etc/passwd /etc/passwd

# Copie du binaire
COPY --from=builder /app/server /server

# Si le service a des assets statiques (ex: frontend minimal), on les copie conditionnellement
# (Note: COPY ne supporte pas de condition, donc on copie tout le dossier s'il existe, 
# mais ici on assume une convention: si un dossier 'web' existe, il est copié)
COPY --from=builder /build/services/${SERVICE_NAME}/web* /static/

# User non-privilégié (Standardisation UID 10001)
USER 10001

EXPOSE 8080

ENTRYPOINT ["/server"]
```

## Makefile

```text
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
```

## k8s/youtube-dl/deployment.yaml

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: youtube-dl
  namespace: apps # Ou ton namespace par défaut
  labels:
    app: youtube-dl
spec:
  replicas: 2 # Scalabilité horizontale (2 pods = 10 téléchargements simultanés au total)
  selector:
    matchLabels:
      app: youtube-dl
  template:
    metadata:
      labels:
        app: youtube-dl
    spec:
      containers:
        - name: youtube-dl
          # Utilise l'image construite par ta CI GitHub Actions
          image: spadmdck/youtube-dl:latest 
          imagePullPolicy: Always
          ports:
            - containerPort: 8080
          env:
            - name: PORT
              value: "8080"
          
          # Healthcheck (Important pour que K8s ne t'envoie pas de trafic si tu es mort)
          livenessProbe:
            httpGet:
              path: /health
              port: 8080
            initialDelaySeconds: 5
            periodSeconds: 10
          
          # LIMITES STRICTES (Comme demandé)
          resources:
            requests:
              memory: "64Mi"
              cpu: "100m"
            limits:
              # Si le pod dépasse 256Mo (très large pour du streaming), K8s le tue.
              memory: "256Mi" 
              # On limite le CPU pour éviter d'affamer les autres services voisins
              cpu: "1000m"
```

## k8s/youtube-dl/ingress.yaml

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: youtube-dl-ingress
  namespace: apps
  annotations:
    # On dit à Traefik que c'est pour lui
    traefik.ingress.kubernetes.io/router.entrypoints: websecure
    traefik.ingress.kubernetes.io/router.tls: "true"
    
    # Middleware RateLimit (Protection Traefik supplémentaire)
    # Si quelqu'un bourrine l'API, Traefik le bloque avant même qu'il touche ton Go
    traefik.ingress.kubernetes.io/router.middlewares: apps-ratelimit@kubernetescrd
spec:
  tls:
    - hosts:
        - flash.dgsynthex.online
      secretName: my-tls-cert # Si tu gères le SSL
  rules:
    - host: flash.dgsynthex.online
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: youtube-dl
                port:
                  number: 80
```

## k8s/youtube-dl/ratelimit.yaml

```yaml
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata:
  name: ratelimit
  namespace: apps
spec:
  rateLimit:
    average: 10  # 10 requêtes par seconde en moyenne
    burst: 20    # Autorise des pics à 20
```

## k8s/youtube-dl/service.yaml

```yaml
apiVersion: v1
kind: Service
metadata:
  name: youtube-dl
  namespace: apps
spec:
  selector:
    app: youtube-dl
  ports:
    - protocol: TCP
      port: 80
      targetPort: 8080
```

## project_export.log

```text
[2025-12-19 00:56:21] Source  : .
[2025-12-19 00:56:21] Sortie  : project_export.md
[2025-12-19 00:56:21] Fichiers trouvés (avant filtre): 24
[2025-12-19 00:56:21] Fichiers à concaténer (après filtre): 24 (exclus auto:0 dir:0 file:0)
[2025-12-19 00:56:21] Concatène [1] .air.toml (size=381)
[2025-12-19 00:56:21] Concatène [2] .github/workflows/ci-cd.yml (size=2094)
[2025-12-19 00:56:21] Concatène [3] Dockerfile (size=2067)
[2025-12-19 00:56:21] Concatène [4] Makefile (size=5396)
[2025-12-19 00:56:21] Concatène [5] k8s/youtube-dl/deployment.yaml (size=1343)
[2025-12-19 00:56:21] Concatène [6] k8s/youtube-dl/ingress.yaml (size=872)
[2025-12-19 00:56:21] Concatène [7] k8s/youtube-dl/ratelimit.yaml (size=211)
[2025-12-19 00:56:21] Concatène [8] k8s/youtube-dl/service.yaml (size=180)

```

## services/bank-parser/go.mod

```text
module github.com/spadmdck/micro-farm/services/bank-parser

go 1.25.1

require github.com/aclindsa/ofxgo v0.1.3

require (
	github.com/aclindsa/xml v0.0.0-20201125035057-bbd5c9ec99ac // indirect
	golang.org/x/text v0.3.7 // indirect
)

```

## services/bank-parser/go.sum

```text
github.com/aclindsa/ofxgo v0.1.3 h1:20Ckjpg5gG4rdGh2juGfa5I1gnWULMXGWxpseVLCVaM=
github.com/aclindsa/ofxgo v0.1.3/go.mod h1:q2mYxGiJr5X3rlyoQjQq+qqHAQ8cTLntPOtY0Dq0pzE=
github.com/aclindsa/xml v0.0.0-20201125035057-bbd5c9ec99ac h1:xCNSfPWpcx3Sdz/+aB/Re4L8oA6Y4kRRRuTh1CHCDEw=
github.com/aclindsa/xml v0.0.0-20201125035057-bbd5c9ec99ac/go.mod h1:GjqOUT8xlg5+T19lFv6yAGNrtMKkZ839Gt4e16mBXlY=
golang.org/x/sys v0.0.0-20210615035016-665e8c7367d1/go.mod h1:oPkhp1MJrh7nUepCBck5+mAzfO9JrbApNNgaTdGDITg=
golang.org/x/term v0.0.0-20210927222741-03fcf44c2211/go.mod h1:jbD1KX2456YbFQfuXm/mYQcufACuNUgVhRMnK/tPxf8=
golang.org/x/text v0.3.7 h1:olpwvP2KacW1ZWvsR7uQhoyTYvKAupfQrRGBFM352Gk=
golang.org/x/text v0.3.7/go.mod h1:u+2+/6zg+i71rQMx5EYifcz6MCKuco9NR6JIITiCfzQ=
golang.org/x/tools v0.0.0-20180917221912-90fa682c2a6e/go.mod h1:n7NCudcB/nEzxVGmLbDWY5pfWTLqBcC2KZ6jyYvM4mQ=

```

## services/bank-parser/main.go

```go
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

```

## services/geo-tools/go.mod

```text
module github.com/spadmdck/micro-farm/services/geo-tools

go 1.25.1

```

## services/geo-tools/main.go

```go
package geotools

```

## services/img-opt/go.mod

```text
module github.com/spadmdck/micro-farm/services/img-opt

go 1.25.1

```

## services/img-opt/main.go

```go
package pdfgen

```

## services/pdf-gen/go.mod

```text
module github.com/spadmdck/micro-farm/services/pdf-gen

go 1.25.1

```

## services/pdf-gen/main.go

```go
package pdfgen

```

## services/qrcode-gen/go.mod

```text
module github.com/spadmdck/micro-farm/services/qrcode-gen

go 1.25.1

```

## services/qrcode-gen/main.go

```go
package pdfgen

```

## services/youtube-dl/go.mod

```text
module github.com/spadmdck/micro-farm/services/youtube-dl

go 1.25.1

require github.com/kkdai/youtube/v2 v2.10.5

require (
	github.com/bitly/go-simplejson v0.5.1 // indirect
	github.com/dlclark/regexp2 v1.11.5 // indirect
	github.com/dop251/goja v0.0.0-20250125213203-5ef83b82af17 // indirect
	github.com/go-sourcemap/sourcemap v2.1.4+incompatible // indirect
	github.com/google/pprof v0.0.0-20250208200701-d0013a598941 // indirect
	golang.org/x/text v0.22.0 // indirect
)

```

## services/youtube-dl/go.sum

```text
github.com/Masterminds/semver/v3 v3.2.1 h1:RN9w6+7QoMeJVGyfmbcgs28Br8cvmnucEXnY0rYXWg0=
github.com/Masterminds/semver/v3 v3.2.1/go.mod h1:qvl/7zhW3nngYb5+80sSMF+FG2BjYrf8m9wsX0PNOMQ=
github.com/bitly/go-simplejson v0.5.1 h1:xgwPbetQScXt1gh9BmoJ6j9JMr3TElvuIyjR8pgdoow=
github.com/bitly/go-simplejson v0.5.1/go.mod h1:YOPVLzCfwK14b4Sff3oP1AmGhI9T9Vsg84etUnlyp+Q=
github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc h1:U9qPSI2PIWSS1VwoXQT9A3Wy9MM3WgvqSxFWenqJduM=
github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc/go.mod h1:J7Y8YcW2NihsgmVo/mv3lAwl/skON4iLHjSsI+c5H38=
github.com/dlclark/regexp2 v1.11.5 h1:Q/sSnsKerHeCkc/jSTNq1oCm7KiVgUMZRDUoRu0JQZQ=
github.com/dlclark/regexp2 v1.11.5/go.mod h1:DHkYz0B9wPfa6wondMfaivmHpzrQ3v9q8cnmRbL6yW8=
github.com/dop251/goja v0.0.0-20250125213203-5ef83b82af17 h1:spJaibPy2sZNwo6Q0HjBVufq7hBUj5jNFOKRoogCBow=
github.com/dop251/goja v0.0.0-20250125213203-5ef83b82af17/go.mod h1:MxLav0peU43GgvwVgNbLAj1s/bSGboKkhuULvq/7hx4=
github.com/go-sourcemap/sourcemap v2.1.4+incompatible h1:a+iTbH5auLKxaNwQFg0B+TCYl6lbukKPc7b5x0n1s6Q=
github.com/go-sourcemap/sourcemap v2.1.4+incompatible/go.mod h1:F8jJfvm2KbVjc5NqelyYJmf/v5J0dwNLS2mL4sNA1Jg=
github.com/google/pprof v0.0.0-20250208200701-d0013a598941 h1:43XjGa6toxLpeksjcxs1jIoIyr+vUfOqY2c6HB4bpoc=
github.com/google/pprof v0.0.0-20250208200701-d0013a598941/go.mod h1:vavhavw2zAxS5dIdcRluK6cSGGPlZynqzFM8NdvU144=
github.com/kkdai/youtube/v2 v2.10.5 h1:22v6qas+/gEhZVmkqAa8fBsLhUsJA5HPDA+mSFkUBwo=
github.com/kkdai/youtube/v2 v2.10.5/go.mod h1:pm4RuJ2tRIIaOvz4YMIpCY8Ls4Fm7IVtnZQyule61MU=
github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 h1:Jamvg5psRIccs7FGNTlIRMkT8wgtp5eCXdBlqhYGL6U=
github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2/go.mod h1:iKH77koFhYxTK1pcRnkKkqfTogsbg7gZNVY4sRDYZ/4=
github.com/stretchr/testify v1.10.0 h1:Xv5erBjTwe/5IxqUQTdXv5kgmIvbHo3QQyRwhJsOfJA=
github.com/stretchr/testify v1.10.0/go.mod h1:r2ic/lqez/lEtzL7wO/rwa5dbSLXVDPFyf8C91i36aY=
golang.org/x/net v0.35.0 h1:T5GQRQb2y08kTAByq9L4/bz8cipCdA8FbRTXewonqY8=
golang.org/x/net v0.35.0/go.mod h1:EglIi67kWsHKlRzzVMUD93VMSWGFOMSZgxFjparz1Qk=
golang.org/x/text v0.22.0 h1:bofq7m3/HAFvbF51jz3Q9wLg3jkvSPuiZu/pD1XwgtM=
golang.org/x/text v0.22.0/go.mod h1:YRoo4H8PVmsu+E3Ou7cqLVH8oXWIHVoX0jqUWALQhfY=
gopkg.in/yaml.v2 v2.4.0 h1:D8xgwECY7CYvx+Y2n4sBz93Jn9JRvxdiyyo8CTfuKaY=
gopkg.in/yaml.v2 v2.4.0/go.mod h1:RDklbk79AGWmwhnvt/jBztapEOGDOx6ZbXqjP6csGnQ=
gopkg.in/yaml.v3 v3.0.1 h1:fxVm/GzAzEWqLHuvctI91KS9hhNmmWOoWu0XTYJS7CA=
gopkg.in/yaml.v3 v3.0.1/go.mod h1:K4uyk7z7BCEPqu6E+C64Yfv1cQ7kz7rIZviUmN+EgEM=

```

## services/youtube-dl/main.go

```go
package main

import (
	"context"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/kkdai/youtube/v2"
)

//go:embed web/*
var webFS embed.FS

// CONFIGURATION DE LA FERME
const (
	MaxConcurrentDownloads = 5                // Nombre de téléchargements actifs simultanés
	MaxQueueSize           = 20               // Nombre de clients autorisés à attendre
	QueueTimeout           = 30 * time.Second // Temps max d'attente dans la file
)

// Le sémaphore limite les actions simultanées
var semaphore = make(chan struct{}, MaxConcurrentDownloads)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// 1. Frontend
	contentStatic, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	http.Handle("/", http.FileServer(http.FS(contentStatic)))

	// 2. API avec Middleware de Queue
	http.HandleFunc("/api/stream", queueMiddleware(streamHandler))

	// 3. Healthcheck pour K8s
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	log.Printf("📺 YouTube Service (Queue: %d, Slots: %d) on :%s", MaxQueueSize, MaxConcurrentDownloads, port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal(err)
	}
}

// Middleware qui gère la file d'attente
func queueMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// A. On essaie d'entrer dans la file d'attente
		// On utilise un select non-bloquant pour voir si le serveur est saturé
		if len(semaphore) >= MaxConcurrentDownloads {
			// Si on est déjà au max d'actifs, est-ce qu'on accepte l'attente ?
			// Ici, on fait une logique simple : Go gère très bien les goroutines en attente.
			// Mais pour éviter d'avoir 1000 connexions ouvertes qui attendent, on peut check une limite logique.
			// Pour cet exemple, on laisse Go gérer l'attente mais avec un Timeout strict.
		}

		// Context pour l'annulation (si le client ferme l'onglet) + Timeout d'attente
		ctx, cancel := context.WithTimeout(r.Context(), QueueTimeout)
		defer cancel()

		log.Printf("[Queue] Client %s demande un ticket...", r.RemoteAddr)

		select {
		case semaphore <- struct{}{}:
			// B. TICKET OBTENU ! On traite la requête.
			// On libérera le ticket à la fin du traitement
			defer func() { <-semaphore }()
			log.Printf("[Start] Client %s commence le téléchargement", r.RemoteAddr)
			next(w, r)
			log.Printf("[End] Client %s a fini", r.RemoteAddr)

		case <-ctx.Done():
			// C. TROP LONG ou ANNULÉ
			log.Printf("[Drop] Client %s a abandonné ou timeout", r.RemoteAddr)
			http.Error(w, "Serveur trop occupé (File d'attente pleine ou temps dépassé)", http.StatusServiceUnavailable)
			return
		}
	}
}

func streamHandler(w http.ResponseWriter, r *http.Request) {
	videoID := r.URL.Query().Get("v")
	if videoID == "" {
		http.Error(w, "Missing video ID", http.StatusBadRequest)
		return
	}

	client := youtube.Client{}
	video, err := client.GetVideo(videoID)
	if err != nil {
		log.Printf("Error GetVideo: %v", err)
		http.Error(w, "Video not found", http.StatusNotFound)
		return
	}

	formats := video.Formats.WithAudioChannels().Type("video/mp4")
	if len(formats) == 0 {
		http.Error(w, "No MP4 format found", http.StatusInternalServerError)
		return
	}

	// Récupération du stream
	stream, size, err := client.GetStream(video, &formats[0])
	if err != nil {
		log.Printf("Error GetStream: %v", err)
		http.Error(w, "Stream error", http.StatusInternalServerError)
		return
	}
	defer stream.Close()

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q.mp4", video.Title))
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", size))

	// Streaming avec protection de coupure
	// Si le client coupe, le io.Copy s'arrête et libère le sémaphore
	if _, err := io.Copy(w, stream); err != nil {
		log.Printf("Connection closed during stream: %v", err)
	}
}

```

## services/youtube-dl/web/index.html

```html
<!DOCTYPE html>
<html lang="fr" class="dark">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>SaaS Media Downloader</title>
    
    <script src="https://cdn.tailwindcss.com"></script>
    
    <script>
        tailwind.config = {
            darkMode: 'class',
            theme: {
                extend: {
                    colors: {
                        brand: {
                            500: '#FF0000', // YouTube Red
                            600: '#CC0000',
                        },
                        dark: {
                            bg: '#0f172a',
                            surface: '#1e293b',
                        }
                    },
                    animation: {
                        'spin-slow': 'spin 3s linear infinite',
                    }
                }
            }
        }
    </script>

    <link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;600;800&display=swap" rel="stylesheet">
    <link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Material+Symbols+Outlined:opsz,wght,FILL,GRAD@24,400,0,0" />

    <style>
        body { font-family: 'Inter', sans-serif; }
        
        /* Effet Glassmorphism */
        .glass {
            background: rgba(30, 41, 59, 0.7);
            backdrop-filter: blur(12px);
            -webkit-backdrop-filter: blur(12px);
            border: 1px solid rgba(255, 255, 255, 0.1);
        }
        
        /* Animation de chargement */
        .loader {
            border-top-color: #FF0000;
            -webkit-animation: spinner 1.5s linear infinite;
            animation: spinner 1.5s linear infinite;
        }
        @keyframes spinner {
            0% { transform: rotate(0deg); }
            100% { transform: rotate(360deg); }
        }
    </style>
</head>

<body class="bg-dark-bg text-white h-screen flex flex-col overflow-hidden relative selection:bg-brand-500 selection:text-white">

    <div class="absolute top-0 left-0 w-full h-full overflow-hidden -z-10 pointer-events-none">
        <div class="absolute top-[-10%] left-[-10%] w-[500px] h-[500px] bg-brand-500/20 rounded-full blur-[100px] mix-blend-screen"></div>
        <div class="absolute bottom-[-10%] right-[-10%] w-[500px] h-[500px] bg-blue-500/10 rounded-full blur-[100px] mix-blend-screen"></div>
    </div>

    <header class="w-full p-6 flex justify-between items-center z-10">
        <div class="flex items-center gap-2">
            <div class="w-8 h-8 bg-brand-500 rounded-lg flex items-center justify-center shadow-lg shadow-brand-500/30">
                <span class="material-symbols-outlined text-white text-sm">play_arrow</span>
            </div>
            <span class="font-bold text-lg tracking-tight">MicroDl</span>
        </div>
        <div class="text-xs font-medium text-slate-400 border border-slate-700 px-3 py-1 rounded-full">
            v1.0.0 • Go Powered
        </div>
    </header>

    <main class="flex-1 flex flex-col items-center justify-center p-4 w-full max-w-2xl mx-auto z-10">
        
        <div class="text-center mb-10 space-y-4">
            <h1 class="text-4xl md:text-5xl font-extrabold text-transparent bg-clip-text bg-gradient-to-r from-white to-slate-400 pb-2">
                Téléchargeur Ultra-Rapide
            </h1>
            <p class="text-slate-400 text-lg max-w-lg mx-auto leading-relaxed">
                Sauvegardez vos vidéos favorites instantanément. <br>
                <span class="text-slate-500 text-sm">Pas de pubs. Pas de trackers. Streaming direct.</span>
            </p>
        </div>

        <div class="w-full glass rounded-2xl p-2 shadow-2xl shadow-black/50 transition-all duration-300 focus-within:ring-2 focus-within:ring-brand-500/50 focus-within:border-brand-500/50 border border-slate-700 relative">
            
            <form id="dlForm" class="flex flex-col md:flex-row gap-2" onsubmit="handleDownload(event)">
                <div class="relative flex-1 group">
                    <div class="absolute inset-y-0 left-0 pl-3 flex items-center pointer-events-none">
                        <span class="material-symbols-outlined text-slate-500 group-focus-within:text-brand-500 transition-colors">link</span>
                    </div>
                    <input 
                        type="text" 
                        id="videoUrl" 
                        placeholder="Collez le lien YouTube ici..." 
                        class="block w-full pl-10 pr-12 py-4 bg-transparent text-white placeholder-slate-500 border-none focus:ring-0 text-base font-medium"
                        autocomplete="off"
                    >
                    <button type="button" id="pasteBtn" class="absolute inset-y-0 right-0 pr-3 flex items-center text-slate-500 hover:text-white transition-colors" title="Coller">
                        <span class="material-symbols-outlined text-sm">content_paste</span>
                    </button>
                </div>

                <button 
                    type="submit" 
                    id="submitBtn"
                    class="bg-gradient-to-br from-brand-500 to-brand-600 hover:from-brand-600 hover:to-brand-500 text-white font-bold py-3 px-8 rounded-xl transition-all transform active:scale-95 shadow-lg shadow-brand-500/25 flex items-center justify-center gap-2 min-w-[160px]"
                >
                    <span id="btnText">Télécharger</span>
                    <span id="btnIcon" class="material-symbols-outlined">download</span>
                    <div id="btnSpinner" class="hidden loader ease-linear rounded-full border-2 border-t-2 border-white/20 h-5 w-5"></div>
                </button>
            </form>

        </div>

        <div id="statusMsg" class="mt-6 h-6 text-sm font-medium transition-all opacity-0 transform translate-y-2 text-center"></div>

    </main>

    <footer class="w-full p-6 text-center text-slate-600 text-xs">
        &copy; 2025 MicroFarm SaaS. Dockerized & Optimized.
    </footer>

    <script>
        const form = document.getElementById('dlForm');
        const input = document.getElementById('videoUrl');
        const submitBtn = document.getElementById('submitBtn');
        const btnText = document.getElementById('btnText');
        const btnIcon = document.getElementById('btnIcon');
        const btnSpinner = document.getElementById('btnSpinner');
        const statusMsg = document.getElementById('statusMsg');
        const pasteBtn = document.getElementById('pasteBtn');

        // Regex pour extraire l'ID Youtube (Supporte short links, embeds, etc.)
        const ytRegex = /(?:youtube\.com\/(?:[^\/]+\/.+\/|(?:v|e(?:mbed)?)\/|.*[?&]v=)|youtu\.be\/)([^"&?\/\s]{11})/i;

        // Gestion du bouton "Coller"
        pasteBtn.addEventListener('click', async () => {
            try {
                const text = await navigator.clipboard.readText();
                input.value = text;
                input.focus();
            } catch (err) {
                showStatus('Impossible d\'accéder au presse-papier', 'text-red-500');
            }
        });

        function setLoading(isLoading) {
            if (isLoading) {
                submitBtn.disabled = true;
                submitBtn.classList.add('opacity-75', 'cursor-not-allowed');
                btnText.innerText = "Traitement...";
                btnIcon.classList.add('hidden');
                btnSpinner.classList.remove('hidden');
            } else {
                submitBtn.disabled = false;
                submitBtn.classList.remove('opacity-75', 'cursor-not-allowed');
                btnText.innerText = "Télécharger";
                btnIcon.classList.remove('hidden');
                btnSpinner.classList.add('hidden');
            }
        }

        function showStatus(msg, colorClass) {
            statusMsg.innerText = msg;
            statusMsg.className = `mt-6 h-6 text-sm font-medium transition-all transform translate-y-0 opacity-100 text-center ${colorClass}`;
            
            // Auto hide après 5s
            setTimeout(() => {
                statusMsg.classList.add('opacity-0', 'translate-y-2');
            }, 5000);
        }

        async function handleDownload(e) {
            e.preventDefault();
            const url = input.value.trim();

            if (!url) {
                showStatus('Veuillez entrer une URL valide.', 'text-red-400');
                input.focus();
                return;
            }

            const match = url.match(ytRegex);
            if (!match || !match[1]) {
                showStatus('Lien YouTube non reconnu.', 'text-red-400');
                return;
            }

            const videoID = match[1];
            
            // UI Loading
            setLoading(true);
            showStatus('Connexion aux serveurs de streaming...', 'text-blue-400');

            // Astuce UX: On attend un tout petit peu pour montrer que ça bosse, 
            // puis on déclenche le téléchargement navigateur
            setTimeout(() => {
                // Déclenchement du téléchargement via l'API Go
                // Le navigateur va gérer le stream comme un téléchargement de fichier
                window.location.href = `/api/stream?v=${videoID}`;
                
                // On reset l'UI
                setLoading(false);
                showStatus('Téléchargement lancé ! 🚀', 'text-green-400');
                input.value = ''; // Clean input
            }, 800);
        }
    </script>
</body>
</html>
```

