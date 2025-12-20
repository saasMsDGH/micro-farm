# 🚜 Go Micro Farm - Orchestrator

Ce projet est un mono-repo Go conçu pour orchestrer des micro-services via un pipeline de déploiement atomique sur Kubernetes.

## 📂 Structure
- **`services/`** : Code source des applications (ex: `youtube-dl`).
- **`k8s/`** : Manifestes Kubernetes par service.
- **`versions.yaml`** : Fichier pivot de la plateforme (Source de vérité des versions).

## 🚀 Cinématique de Déploiement Atomique
La CI/CD repose sur un principe simple : **Un seul commit contient le code et le changement de version.**

1. **Développement local** : Modifiez le code dans `services/`.
2. **Versioning** : Exécutez `make patch s=nom-service` pour mettre à jour `versions.yaml`.
3. **Commit Unique** : `git commit -am "fix: description"`.
4. **Pipeline unique** :
    - **Check** : Validation de tous les modules Go.
    - **Build** : Détecte le changement de version, build l'image Docker et la push.
    - **Deploy** : Injecte l'image et déploie sur le cluster via le runner self-hosted.

## 🛠️ Makefile
- `make patch s=x` : Incrémente la version patch.
- `make minor s=x` : Incrémente la version mineure.
- `make dev service=x` : Lancement local avec Hot Reload (Air).
- `make tidy-all` : Nettoie les dépendances Go de tous les services.

---
*DGSynthex - Orchestrateur Micro-services*