# 🚜 Go Micro Farm - Orchestrator

Ce projet est un mono-repo Go conçu pour orchestrer des micro-services avec une gestion GitOps intégrale.

## 📂 Structure
- **`services/`** : Code source des applications (ex: `youtube-dl`).
- **`k8s/`** : Déploiements Kubernetes par service.
- **`versions.yaml`** : Source de vérité des versions déployées.

## 🚀 Cinématique CI/CD
Le flux est entièrement automatisé via GitHub Actions :

1. **Commit** : Un push sur `master` avec un préfixe (`fix:`, `feat:`) déclenche le **Bumper**.
2. **Bumper** : Incrémente `versions.yaml` selon le type de commit et push le changement.
3. **Tagger** : Détecte le changement dans `versions.yaml` et crée un tag Git (ex: `youtube-dl@v0.1.9`).
4. **Pipeline** : Détecte le tag, construit l'image Docker et déploie sur le cluster.

## 🛠️ Makefile
- `make dev service=x` : Développement avec Hot Reload.
- `make init-all` : Initialise les modules Go.
- `make tag service=x v=1.0.0` : Forcer un tag manuellement.
- `make untag service=x v=1.0.0` : Supprimer un tag proprement.

## 🍪 youtube-dl
Service de streaming avec muxing FFmpeg. Utilise `/etc/youtube-dl/cookies.txt` pour bypasser les restrictions.