#!/usr/bin/env bash
set -e

# Nome do executável de saída (padrão: web-remote-controller)
OUTPUT_BIN="${1:-web-remote-controller}"
IMAGE="ubuntu:20.04"
TARGET_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "=== Compilando $OUTPUT_BIN para compatibilidade com Ubuntu 20.04 (GLIBC 2.31) ==="

# Verifica se o docker está disponível
if ! command -v docker >/dev/null 2>&1; then
    echo "Erro: Docker não encontrado. Certifique-se de que o Docker está instalado e o daemon em execução."
    exit 1
fi

USER_UID=$(id -u)
USER_GID=$(id -g)

# Detecta se há Go instalado no host para reaproveitar e acelerar o processo
GO_MOUNT=""
GO_SETUP=""

if command -v go >/dev/null 2>&1; then
    LOCAL_GOROOT=$(go env GOROOT 2>/dev/null || true)
    if [ -n "$LOCAL_GOROOT" ] && [ -d "$LOCAL_GOROOT" ]; then
        echo "-> Reaproveitando Go do host: $LOCAL_GOROOT"
        GO_MOUNT="-v $LOCAL_GOROOT:/usr/local/go:ro"
        GO_SETUP="export PATH=\$PATH:/usr/local/go/bin"
    fi
elif [ -d "$HOME/Documents/go" ]; then
    echo "-> Reaproveitando Go do host: $HOME/Documents/go"
    GO_MOUNT="-v $HOME/Documents/go:/usr/local/go:ro"
    GO_SETUP="export PATH=\$PATH:/usr/local/go/bin"
elif [ -d "/usr/local/go" ]; then
    echo "-> Reaproveitando Go do host: /usr/local/go"
    GO_MOUNT="-v /usr/local/go:/usr/local/go:ro"
    GO_SETUP="export PATH=\$PATH:/usr/local/go/bin"
fi

if [ -z "$GO_MOUNT" ]; then
    echo "-> Go não detectado no host. O Go será baixado temporariamente no container."
    GO_SETUP="
      if [ ! -f /usr/local/go/bin/go ]; then
        echo 'Baixando Go 1.24.2...'
        wget -q https://go.dev/dl/go1.24.2.linux-amd64.tar.gz && \
        tar -C /usr/local -xzf go1.24.2.linux-amd64.tar.gz && \
        rm -f go1.24.2.linux-amd64.tar.gz
      fi
      export PATH=\$PATH:/usr/local/go/bin
    "
fi

docker run --rm \
  $GO_MOUNT \
  -v "$TARGET_DIR":/app \
  -w /app \
  -e PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
  -e GOCACHE=/tmp/gocache \
  -e GOPATH=/tmp/gopath \
  "$IMAGE" bash -c "
    set -e
    $GO_SETUP
    echo '-> Atualizando repositórios e instalando dependências ALSA...'
    apt-get update -qq && \
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
      build-essential libasound2-dev ca-certificates wget >/dev/null

    echo '-> Compilando binário...'
    go build -ldflags='-s -w' -o \"$OUTPUT_BIN\" .

    # Restaura permissões para o usuário atual
    chown $USER_UID:$USER_GID \"$OUTPUT_BIN\"
"

echo "=== Sucesso! Executável gerado: $TARGET_DIR/$OUTPUT_BIN ==="
