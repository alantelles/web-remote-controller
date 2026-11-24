#!/usr/bin/env bash
# ==============================================================================
# Empacotador da aplicação para Ubuntu 20.04 (Focal Fossa) - .tar.gz e .deb
# Uso: ./scripts/package-ubuntu2004.sh <versao>
# ==============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$ROOT_DIR"

RAW_VERSION="${1:-0.0.0-dev}"
VERSION="${RAW_VERSION#v}"
PKG_NAME="01v96-controller"
ARCH="amd64"

if [ ! -f "./01v96-controller" ]; then
    echo ">>> Binário ./01v96-controller não encontrado. Executando build-ubuntu2004.sh..."
    ./build-ubuntu2004.sh
fi

rm -rf dist/pkg-tar dist/pkg-deb
mkdir -p dist

# ==============================================================================
# 1. Pacote portátil (.tar.gz) pronto para extrair e executar
# ==============================================================================
TAR_BASENAME="${PKG_NAME}-v${VERSION}-ubuntu20.04-${ARCH}"
TAR_DIR="dist/pkg-tar/${TAR_BASENAME}"
mkdir -p "${TAR_DIR}/data" "${TAR_DIR}/nginx"

cp ./01v96-controller "${TAR_DIR}/01v96-controller"
chmod 755 "${TAR_DIR}/01v96-controller"

cp index.html mobile.html mix.html login.html "${TAR_DIR}/"
cp install_dependencies.sh 01v96-controller.service "${TAR_DIR}/"
chmod 755 "${TAR_DIR}/install_dependencies.sh"
cp -r nginx/* "${TAR_DIR}/nginx/"

if [ -f "usuarios.csv" ]; then
    cp usuarios.csv "${TAR_DIR}/usuarios.csv"
else
    printf "nome,usuario,senha,papel\nAlan,alan,alan,ADMIN\n" > "${TAR_DIR}/usuarios.csv"
fi

echo "${VERSION}" > "${TAR_DIR}/VERSION"

tar -C dist/pkg-tar -czf "dist/${TAR_BASENAME}.tar.gz" "${TAR_BASENAME}"
echo ">>> Criado: dist/${TAR_BASENAME}.tar.gz"

# ==============================================================================
# 2. Pacote Debian/Ubuntu (.deb) com dependências e serviço systemd
# ==============================================================================
DEB_DIR="dist/pkg-deb"
APP_OPT="${DEB_DIR}/opt/${PKG_NAME}"
mkdir -p "${DEB_DIR}/DEBIAN" "${APP_OPT}/data" "${APP_OPT}/nginx" "${DEB_DIR}/usr/local/bin" "${DEB_DIR}/lib/systemd/system"
chmod 0755 "${DEB_DIR}/DEBIAN"

cp ./01v96-controller "${APP_OPT}/01v96-controller"
chmod 755 "${APP_OPT}/01v96-controller"

cp index.html mobile.html mix.html login.html "${APP_OPT}/"
cp install_dependencies.sh "${APP_OPT}/"
chmod 755 "${APP_OPT}/install_dependencies.sh"
cp -r nginx/* "${APP_OPT}/nginx/"
echo "${VERSION}" > "${APP_OPT}/VERSION"

cat > "${DEB_DIR}/usr/local/bin/${PKG_NAME}" << 'EOF'
#!/usr/bin/env bash
cd /opt/01v96-controller || exit 1
exec ./01v96-controller "$@"
EOF
chmod 755 "${DEB_DIR}/usr/local/bin/${PKG_NAME}"

cat > "${APP_OPT}/run-service.sh" << 'EOF'
#!/usr/bin/env bash
set -euo pipefail
cd /opt/01v96-controller

HTTP_PORT="${HTTP_PORT:-9553}"
MIDI_PORT="${MIDI_PORT:-Midi Through}"

if [[ "$HTTP_PORT" != *:* ]]; then
    HTTP_ADDR=":${HTTP_PORT}"
else
    HTTP_ADDR="${HTTP_PORT}"
fi

if [ "$MIDI_PORT" = "sim" ] || [ "$MIDI_PORT" = "s" ] || [ "$MIDI_PORT" = "none" ]; then
    exec ./01v96-controller -addr "$HTTP_ADDR" -sim
else
    exec ./01v96-controller -addr "$HTTP_ADDR" -port "$MIDI_PORT"
fi
EOF
chmod 755 "${APP_OPT}/run-service.sh"

cat > "${DEB_DIR}/lib/systemd/system/${PKG_NAME}.service" << 'EOF'
[Unit]
Description=01V96 Remote Controller (Yamaha 01V96 / 01V96V2 Web Remote)
After=network.target sound.target

[Service]
Type=simple
Group=audio
WorkingDirectory=/opt/01v96-controller
EnvironmentFile=-/etc/default/01v96-controller
ExecStart=/opt/01v96-controller/run-service.sh
Restart=always
RestartSec=3
LimitNOFILE=65536
Environment=LANG=pt_BR.UTF-8

[Install]
WantedBy=multi-user.target
EOF

cat > "${DEB_DIR}/DEBIAN/control" << EOF
Package: ${PKG_NAME}
Version: ${VERSION}
Section: sound
Priority: optional
Architecture: ${ARCH}
Depends: debconf (>= 0.5) | debconf-2.0, libasound2, alsa-utils, ca-certificates
Recommends: nginx
Maintainer: Alan Telles https://github.com/alantelles
Description: Web remote controller for Yamaha 01V96 / 01V96V2 digital mixer
 Real-time web remote control (FOH and monitors/auxiliaries) for Yamaha
 01V96 / 01V96V2 consoles via native ALSA MIDI SysEx messages.
EOF

cat > "${DEB_DIR}/DEBIAN/templates" << 'EOF'
Template: 01v96-controller/http-port
Type: string
Default: 9553
Description: Porta HTTP do servidor web (01v96-controller):
 Informe a porta TCP em que a interface web sera executada (padrao: 9553).

Template: 01v96-controller/no-01v96-retry
Type: boolean
Default: false
Description: ATENCAO: Nenhuma porta MIDI com "01V96" foi encontrada!
 Nenhuma porta MIDI contendo "01V96" no nome foi detectada neste computador.
 Para configurar a mesa corretamente, ela deve estar ligada e conectada via
 USB/MIDI agora (as portas criadas pela mesa contem "01V96" no nome).
 .
 Deseja ligar/conectar a mesa agora e atualizar a listagem de portas?

Template: 01v96-controller/midi-port
Type: select
Choices: ${CHOICES}
Description: Selecione a porta MIDI da Yamaha 01V96:
 A mesa deve estar ligada e conectada durante a instalacao para aparecer
 na lista abaixo. A porta escolhida sera salva em /etc/default/01v96-controller
 e reutilizada automaticamente nas proximas inicializacoes.
 .
 Caso a porta configurada nao esteja disponivel em um boot futuro, o servico
 nao iniciara em modo simulacao (ficara aguardando a mesa ser ligada).

Template: 01v96-controller/midi-port-manual
Type: string
Default: Midi Through
Description: Informe manualmente o nome ou indice da porta MIDI:
 Digite o nome (ou parte dele, ex: 01V96, Midi Through, Fast Track) ou o
 indice numerico da porta ALSA. Use "sim" apenas para testes em simulacao.

Template: 01v96-controller/admin-name
Type: string
Default: Administrador
Description: Nome de exibicao do usuario administrador inicial:
 Informe o nome completo ou de exibicao do primeiro usuario ADMIN
 que sera gravado em /opt/01v96-controller/usuarios.csv.

Template: 01v96-controller/admin-user
Type: string
Default: admin
Description: Login (usuario) do administrador inicial:
 Informe o nome de usuario usado para fazer login na interface web (padrao: admin).

Template: 01v96-controller/admin-password
Type: password
Default: admin
Description: Senha do administrador inicial:
 Informe a senha do usuario administrador inicial (padrao se vazio: admin).
EOF

cat > "${DEB_DIR}/DEBIAN/config" << 'EOF'
#!/usr/bin/env bash
set -e
. /usr/share/debconf/confmodule

if [ -f /etc/default/01v96-controller ]; then
    # shellcheck disable=SC1091
    . /etc/default/01v96-controller || true
    [ -n "${HTTP_PORT:-}" ] && db_set 01v96-controller/http-port "$HTTP_PORT" || true
fi

db_input high 01v96-controller/http-port || true
db_go || true

if [ ! -f /opt/01v96-controller/usuarios.csv ]; then
    db_input high 01v96-controller/admin-name || true
    db_input high 01v96-controller/admin-user || true
    db_input high 01v96-controller/admin-password || true
    db_go || true
fi

# Se aconnect ainda nao estiver instalado durante o preconfigure, aguarda o postinst
# (quando confmodule reexecuta config ja com alsa-utils instalado).
if ! command -v aconnect >/dev/null 2>&1; then
    exit 0
fi

# Se a porta MIDI ja foi selecionada nesta sessao (ex: durante preconfigure), nao re-perguntar
db_fget 01v96-controller/midi-port seen || true
if [ "${RET:-false}" = "true" ] && [ "${1:-}" != "reconfigure" ]; then
    exit 0
fi

MAX_RETRIES=10
RETRY_COUNT=0
while [ "$RETRY_COUNT" -lt "$MAX_RETRIES" ]; do
    PORTS=$(aconnect -o 2>/dev/null | awk -F"'" '/^[[:space:]]+[0-9]+/ && NF >= 2 { gsub(/,/, " ", $2); print $2 }' || true)

    if ! printf '%s\n' "$PORTS" | grep -qi "01v96"; then
        db_input high 01v96-controller/no-01v96-retry || true
        db_go || true
        db_get 01v96-controller/no-01v96-retry || true
        if [ "${RET:-false}" = "true" ]; then
            db_reset 01v96-controller/no-01v96-retry || true
            RETRY_COUNT=$((RETRY_COUNT + 1))
            continue
        fi
    fi

    CHOICES=""
    DEFAULT_CHOICE=""
    FIRST_01V96=""
    HAS_THROUGH=""

    while IFS= read -r port; do
        [ -z "$port" ] && continue
        if [ -z "$CHOICES" ]; then
            CHOICES="$port"
        else
            CHOICES="${CHOICES}, ${port}"
        fi
        if [ -z "$FIRST_01V96" ] && printf '%s' "$port" | grep -qi "01v96"; then
            FIRST_01V96="$port"
        fi
        if [ -z "$HAS_THROUGH" ] && printf '%s' "$port" | grep -qi "midi through"; then
            HAS_THROUGH="$port"
        fi
    done <<< "$PORTS"

    if [ -n "$CHOICES" ]; then
        CHOICES="${CHOICES}, Atualizar lista de portas (re-escanear), Digitar manualmente..., sim (apenas simulacao/testes)"
    else
        CHOICES="Midi Through, Atualizar lista de portas (re-escanear), Digitar manualmente..., sim (apenas simulacao/testes)"
    fi

    if [ -n "$FIRST_01V96" ]; then
        DEFAULT_CHOICE="$FIRST_01V96"
    elif [ -n "$HAS_THROUGH" ]; then
        DEFAULT_CHOICE="$HAS_THROUGH"
    else
        DEFAULT_CHOICE="Midi Through"
    fi

    db_subst 01v96-controller/midi-port CHOICES "$CHOICES"
    db_get 01v96-controller/midi-port || true
    if [ -z "${RET:-}" ] || [ "${RET:-}" = "Atualizar lista de portas (re-escanear)" ]; then
        db_set 01v96-controller/midi-port "$DEFAULT_CHOICE"
    fi
    db_input high 01v96-controller/midi-port || true
    db_go || true
    db_get 01v96-controller/midi-port || true

    if [ "${RET:-}" = "Atualizar lista de portas (re-escanear)" ]; then
        db_reset 01v96-controller/midi-port || true
        db_reset 01v96-controller/no-01v96-retry || true
        RETRY_COUNT=$((RETRY_COUNT + 1))
        continue
    fi

    if [ "${RET:-}" = "Digitar manualmente..." ]; then
        db_input high 01v96-controller/midi-port-manual || true
        db_go || true
    fi
    break
done
EOF
chmod 755 "${DEB_DIR}/DEBIAN/config"

cat > "${DEB_DIR}/DEBIAN/postinst" << 'EOF'
#!/usr/bin/env bash
set -e
. /usr/share/debconf/confmodule

mkdir -p /opt/01v96-controller/data

db_get 01v96-controller/http-port || true
HTTP_PORT="${RET:-9553}"
[ -z "$HTTP_PORT" ] && HTTP_PORT="9553"

db_get 01v96-controller/midi-port || true
SELECTED_PORT="${RET:-}"

if [ "$SELECTED_PORT" = "Digitar manualmente..." ]; then
    db_get 01v96-controller/midi-port-manual || true
    MIDI_PORT="${RET:-Midi Through}"
elif [ "$SELECTED_PORT" = "sim (apenas simulacao/testes)" ]; then
    MIDI_PORT="sim"
elif [ -z "$SELECTED_PORT" ] || [ "$SELECTED_PORT" = "Atualizar lista de portas (re-escanear)" ]; then
    MIDI_PORT="Midi Through"
else
    MIDI_PORT="$SELECTED_PORT"
fi

cat > /etc/default/01v96-controller << CONF
# Configuracao do servico 01v96-controller
# Para reconfigurar via assistente: sudo dpkg-reconfigure 01v96-controller
HTTP_PORT="${HTTP_PORT}"
MIDI_PORT="${MIDI_PORT}"
CONF

if [ ! -f /opt/01v96-controller/usuarios.csv ]; then
    db_get 01v96-controller/admin-name || true
    ADMIN_NAME="${RET:-Administrador}"
    ADMIN_NAME="${ADMIN_NAME//,/ }"

    db_get 01v96-controller/admin-user || true
    ADMIN_USER="${RET:-admin}"
    ADMIN_USER="${ADMIN_USER//,/}"
    [ -z "$ADMIN_USER" ] && ADMIN_USER="admin"

    db_get 01v96-controller/admin-password || true
    ADMIN_PASS="${RET:-admin}"
    ADMIN_PASS="${ADMIN_PASS//,/}"
    [ -z "$ADMIN_PASS" ] && ADMIN_PASS="admin"

    printf "nome,usuario,senha,papel,mix,canais,saidas\n%s,%s,%s,ADMIN,,,\n" \
        "$ADMIN_NAME" "$ADMIN_USER" "$ADMIN_PASS" > /opt/01v96-controller/usuarios.csv
    chmod 640 /opt/01v96-controller/usuarios.csv

    # Limpa a senha do cache do debconf apos gravar o CSV
    db_reset 01v96-controller/admin-password || true
fi

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    systemctl daemon-reload || true
    if systemctl is-active --quiet 01v96-controller.service; then
        systemctl restart 01v96-controller.service || true
    fi
fi
EOF
chmod 755 "${DEB_DIR}/DEBIAN/postinst"

cat > "${DEB_DIR}/DEBIAN/prerm" << 'EOF'
#!/usr/bin/env bash
set -e

if [ "${1:-}" = "remove" ] || [ "${1:-}" = "deconfigure" ]; then
    if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
        systemctl stop 01v96-controller.service || true
        systemctl disable 01v96-controller.service || true
    fi
fi
EOF
chmod 755 "${DEB_DIR}/DEBIAN/prerm"

cat > "${DEB_DIR}/DEBIAN/postrm" << 'EOF'
#!/usr/bin/env bash
set -e

if [ "${1:-}" = "purge" ]; then
    rm -f /etc/default/01v96-controller
    rm -f /opt/01v96-controller/usuarios.csv
    if [ -e /usr/share/debconf/confmodule ]; then
        . /usr/share/debconf/confmodule
        db_purge || true
    fi
fi
EOF
chmod 755 "${DEB_DIR}/DEBIAN/postrm"

DEB_FILE="dist/${PKG_NAME}_${VERSION}_${ARCH}.deb"
dpkg-deb --build --root-owner-group "${DEB_DIR}" "${DEB_FILE}"
echo ">>> Criado: ${DEB_FILE}"

rm -rf dist/pkg-tar dist/pkg-deb
ls -lh dist/
