#!/usr/bin/env bash
# ==============================================================================
# Script de Compilação Automatizada via Docker para Ubuntu 20.04 (Focal Fossa)
# ==============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

echo "=== Compilando binário nativo para Ubuntu 20.04 via Docker ==="

if ! command -v docker &>/dev/null; then
    echo "[ERRO] Docker não está instalado no sistema. Instale o Docker para prosseguir."
    exit 1
fi

IMAGE_TAG="01v96-builder:ubuntu20.04"

echo "1. Construindo imagem de build..."
docker build -f Dockerfile.ubuntu2004 -t "$IMAGE_TAG" .

echo "2. Extraindo binário compilado '01v96-controller'..."
CONTAINER_ID=$(docker create "$IMAGE_TAG")
docker cp "${CONTAINER_ID}:/01v96-controller" ./01v96-controller
docker rm "$CONTAINER_ID" >/dev/null

# Ajustar permissões para o usuário atual
chmod +x ./01v96-controller
if [ -n "${SUDO_USER:-}" ]; then
    chown "${SUDO_USER}:${SUDO_USER}" ./01v96-controller
fi

echo "=== Compilação concluída com sucesso! ==="
ls -lh ./01v96-controller
file ./01v96-controller
