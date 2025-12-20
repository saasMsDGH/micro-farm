# 🚜 Go Micro Farm - Orchestrator

Ce projet est un orchestrateur de micro-services basé sur Go, structuré en mono-repo. Il gère l'intégralité du cycle de vie des applications, du développement local au déploiement automatisé sur Kubernetes.

## 📂 Structure du Dépôt

- **`services/`** : Contient les répertoires de chaque micro-service (ex: `youtube-dl`). Chaque service est indépendant avec son propre module Go.
- **`k8s/`** : Manifestes Kubernetes par service (Deployments, Ingress, Secrets, Services).
- **`versions.yaml`** : Fichier pivot de la plateforme. Il suit les versions actuelles de chaque service et sert de déclencheur pour la CI/CD.
- **`Makefile`** : Interface de commande unique pour le projet.
- **`.github/workflows/`** : Logique d'automatisation (Bumper, Tagger, Pipeline).

---

## ⚙️ Cinématique d'Auto-Versioning

Le projet utilise une chaîne d'automatisation pour éviter la gestion manuelle des tags et du fichier `versions.yaml`.



### 1. Commit & Analyse
Lors d'un push sur `master`, le workflow **Auto-Version Bumper** analyse les changements dans le dossier `services/`. Il détermine le niveau de version à incrémenter selon le message du commit :
- `fix:` ➡️ Augmente le **Patch** (0.0.1)
- `feat:` ➡️ Augmente la **Minor** (0.1.0)
- `!:` ou `BREAKING CHANGE` ➡️ Augmente la **Major** (1.0.0)

Le workflow met à jour `versions.yaml` et crée un commit automatique.

### 2. Taggage Automatique
Le workflow **Release Coordinator** surveille les modifications de `versions.yaml`. Lorsqu'une version change, il crée un tag Git au format `nom-du-service@vX.Y.Z` et le pousse sur le dépôt.

### 3. Déploiement
Le workflow **Service Pipeline** se déclenche à chaque création de tag `*@v*`.
1. **Build** : Construction de l'image Docker du service concerné.
2. **Push** : Publication sur le registre Docker.
3. **Deploy** : Mise à jour du cluster Kubernetes (injection de l'image et configuration des secrets).

---

## 🛠️ Utilisation du Makefile

Le `Makefile` simplifie les opérations courantes :

### Développement Local
- `make init-all` : Initialise les modules Go pour tous les services existants.
- `make tidy-all` : Exécute `go mod tidy` récursivement.
- `make dev service=nom-du-service` : Lance un service avec **Hot Reload** (via Air) et libère automatiquement le port s'il est occupé.
- `make create-service name=nouveau-nom` : Génère la structure complète d'un nouveau micro-service Go.

### Gestion Manuelle des Tags
- `make tag service=x v=1.0.0` : Crée et pousse manuellement un tag de version sur le dépôt.
- `make untag service=x v=1.0.0` : Supprime proprement un tag en local et sur le remote.

---

## 📦 Focus Service : youtube-dl

Le service exemple inclus permet le streaming vidéo via une architecture robuste :
- **Muxing HD** : Intégration de `ffmpeg` pour combiner les flux audio et vidéo à la volée.
- **Gestion d'identité** : Utilisation de cookies au format Netscape via Secrets K8s pour contourner les restrictions.
- **Monitoring** : Métriques Prometheus exposées sur `/metrics` et logs structurés en JSON.