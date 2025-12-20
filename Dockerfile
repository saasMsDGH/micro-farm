# ==============================================================================
# BASE BUILDER (Cache les outils communs)
# ==============================================================================
FROM golang:1.25-alpine AS builder

# Arguments passés par le build command (ex: --build-arg SERVICE_NAME=youtube-dl)
ARG SERVICE_NAME

WORKDIR /build

# Installation des dépendances système (Certificats, Git)
RUN apk add --no-cache git ca-certificates tzdata

# 1. On prépare le dossier du service spécifique
# On assume que le build context est la racine du monorepo
COPY services/${SERVICE_NAME}/go.mod services/${SERVICE_NAME}/go.sum ./services/${SERVICE_NAME}/
# Si tu as un dossier pkg partagé à la racine
# COPY pkg/ ./pkg/

# 2. Download des dépendances (Cache Docker optimisé)
WORKDIR /build/services/${SERVICE_NAME}
RUN go mod download

# 3. Copie du code source complet du service
COPY services/${SERVICE_NAME}/ .

# 4. Compilation Statique (Comme dans ton projet Klaro)
# CGO_ENABLED=0 est vital pour scratch
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o /app/server .


# 2. PROVIDER FFMPEG STATIQUE
FROM alpine:latest AS ffmpeg-provider
RUN wget https://johnvansickle.com/ffmpeg/releases/ffmpeg-release-amd64-static.tar.xz \
    && tar xvf ffmpeg-release-amd64-static.tar.xz \
    && mv ffmpeg-*-amd64-static/ffmpeg /usr/local/bin/

# ==============================================================================
# FINAL IMAGE (Scratch - Ultra léger ~10-20Mo)
# ==============================================================================
FROM scratch

ARG SERVICE_NAME

# Import des fichiers système essentiels
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /etc/passwd /etc/passwd
COPY --from=ffmpeg-provider /usr/local/bin/ffmpeg /usr/local/bin/ffmpeg

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