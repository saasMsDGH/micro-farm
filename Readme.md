# 🚜 Go Micro Farm - Orchestrator

Ce dépôt est un **orchestrateur de micro-services** basé sur **Go**, utilisant une **architecture mono-repo** pour la gestion du cycle de vie des applications, du développement local au déploiement **Kubernetes**.

---

## 📂 Structure du projet

* `services/` : contient le code source de chaque micro-service (ex : `youtube-dl`).
  Chaque service possède son **propre module Go** et ses **ressources statiques**.
* `k8s/` : manifestes Kubernetes organisés par service (`Deployment`, `Service`, `Secret`, `Ingress`).
* `versions.yaml` : fichier central de suivi des versions. C’est la **source de vérité** pour le déploiement.
* `.air.toml` : configuration pour le rechargement à chaud (*Hot Reload*) en cours de développement.
* `Makefile` : point d’entrée unique pour l’administration du projet.

---

## ⚙️ CI/CD et cinématique d’automatisation

Le projet implémente un flux **GitOps** automatisé pour la gestion des versions et le déploiement.

### 1) Incrémentation automatique (*Auto-Bumper*)

Le workflow `.github/workflows/auto-version-bumper.yml` analyse les messages de commit pour mettre à jour `versions.yaml` :

* **Fix** (`fix:`) ➜ incrémente le **Patch** (`0.0.x`).
* **Feature** (`feat:`) ➜ incrémente la **Minor** (`0.x.0`).
* **Breaking Change** (`!:` ou `BREAKING CHANGE`) ➜ incrémente la **Major** (`x.0.0`).

### 2) Gestion des tags (`tag.yml`)

Dès que `versions.yaml` est modifié sur la branche `master`, le workflow **Release Coordinator** :

1. Détecte quel service a changé de version.
2. Crée un tag Git au format : `nom-du-service@vX.Y.Z`.
3. Pousse le tag, ce qui déclenche le déploiement.

### 3) Pipeline de déploiement (`service-pipeline.yml`)

Ce workflow réagit aux nouveaux tags :

* **Build** : construction de l’image Docker du service concerné.
* **Push** : envoi de l’image sur le registre Docker.
* **Deploy** : mise à jour du cluster Kubernetes (injection de l’image et des secrets).

---

## 🛠️ Utilisation du Makefile

Le `Makefile` permet d’orchestrer les tâches courantes.

### Développement

* `make init-all` : initialise tous les modules Go (`go mod init/tidy`).
* `make tidy-all` : nettoie les dépendances de tous les services.
* `make create-service name=x` : génère la structure d’un nouveau micro-service.
* `make dev service=x` : lance un service avec *Hot Reload* (Air) et nettoyage automatique du port.

### Gestion des releases (CI/CD)

* `make tag service=x v=1.0.0` : crée et pousse manuellement un tag de version.
* `make untag service=x v=1.0.0` : supprime un tag localement et sur le dépôt distant.

### Administration Docker

* `make docker service=x` : construit l’image Docker locale pour un service.

---

## 🚀 Exemple de service : `youtube-dl`

Le service `youtube-dl` illustre les capacités de la plateforme :

* **Moteur** : utilisation de `kkdai/youtube/v2` pour l’extraction.
* **Muxing HD** : intégration de **FFmpeg** via des *pipes* Go pour combiner les flux audio et vidéo.
* **Sécurité** : gestion des cookies au format **Netscape** via des **Secrets Kubernetes** pour contourner certaines restrictions (âge, embedding).
* **Monitoring** : exposition de métriques Prometheus (`/metrics`) et logs JSON structurés.

---

## 📊 Monitoring

Chaque service expose :

* `GET /health` : pour les sondes de disponibilité (Liveness/Readiness probes).
* `GET /metrics` : pour la collecte Prometheus (ex : `active_downloads`).
* **Logs** : sortie standard en format JSON pour ingestion automatique par Loki.

---

**Généré pour le projet Micro-Farm — DGSynthex**

### Résumé des modifications apportées

* **Strictement technique** : le README ne décrit que le code présent dans `project_export.md`.
* **Explication de la CI** : détail des 3 workflows GitHub présents dans l’export.
* **Anatomie du Makefile** : liste des cibles réelles (y compris `tag`, `untag`, `create-service`).
* **Précision sur le versioning** : explication du lien entre les commits, le fichier `versions.yaml` et les tags Git.
