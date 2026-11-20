#!/usr/bin/env bash
# ==============================================================================
# Script de Instalação de Dependências de Sistema - Ubuntu 20.04 (Focal Fossa)
# Aplicação: 01V96 Remote Controller (Yamaha 01V96 / 01V96V2 Web Remote)
# ==============================================================================

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}===================================================================${NC}"
echo -e "${BLUE}  01V96 Controller - Instalação de Dependências de Sistema (Ubuntu 20.04)  ${NC}"
echo -e "${BLUE}===================================================================${NC}"

# 1. Verificar privilégios de superusuário (root ou sudo)
if [ "$(id -u)" -ne 0 ]; then
    echo -e "${RED}[ERRO] Este script precisa ser executado como root ou via sudo.${NC}"
    echo -e "Exemplo: ${YELLOW}sudo ./install_dependencies.sh${NC}"
    exit 1
fi

TARGET_USER="${SUDO_USER:-$USER}"

echo -e "\n${YELLOW}>>> [1/5] Atualizando repositórios de pacotes (apt-get update)...${NC}"
apt-get update -y

echo -e "\n${YELLOW}>>> [2/5] Instalando bibliotecas ALSA, utilitários MIDI, Nginx e ferramentas...${NC}"
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    libasound2 \
    alsa-utils \
    nginx \
    ca-certificates \
    curl

echo -e "${GREEN}[OK] Pacotes instalados com sucesso:${NC}"
echo "  - libasound2: Biblioteca nativa ALSA para I/O MIDI"
echo "  - alsa-utils: Utilitários aconnect, amidi, aseqdump, aplay"
echo "  - nginx: Servidor Web e Reverse Proxy (para rota /mixer e WebSocket)"
echo "  - ca-certificates: Certificados de autoridade TLS/SSL"

echo -e "\n${YELLOW}>>> [3/5] Configurando permissões de usuário para portas MIDI (grupo audio)...${NC}"
if id "$TARGET_USER" &>/dev/null; then
    usermod -aG audio "$TARGET_USER"
    echo -e "${GREEN}[OK] Usuário '${TARGET_USER}' adicionado ao grupo 'audio'.${NC}"
    echo -e "${BLUE}[INFO] Nota: Se você estiver logado com '${TARGET_USER}', encerre a sessão (logout/login) ou execute 'newgrp audio' para aplicar o novo grupo.${NC}"
else
    echo -e "${YELLOW}[AVISO] Usuário '${TARGET_USER}' não localizado para inclusão no grupo audio.${NC}"
fi

echo -e "\n${YELLOW}>>> [4/5] Configurando módulo de portas MIDI virtuais (snd-virmidi)...${NC}"
# Carrega módulo imediatamente se suportado pelo kernel
if modprobe snd-virmidi 2>/dev/null; then
    echo -e "${GREEN}[OK] Módulo 'snd-virmidi' carregado com sucesso.${NC}"
else
    echo -e "${YELLOW}[AVISO] Não foi possível carregar 'snd-virmidi' (comum dentro de contêineres Docker/LXC).${NC}"
fi

# Persistir carregamento no boot em /etc/modules
MODULE_ENTRY="snd-virmidi"
if [ -f /etc/modules ]; then
    if ! grep -q "^${MODULE_ENTRY}$" /etc/modules; then
        echo "$MODULE_ENTRY" >> /etc/modules
        echo -e "${GREEN}[OK] 'snd-virmidi' adicionado ao /etc/modules para inicialização automática no boot.${NC}"
    else
        echo -e "${GREEN}[OK] 'snd-virmidi' já configurado em /etc/modules.${NC}"
    fi
fi

echo -e "\n${YELLOW}>>> [5/5] Verificação das portas MIDI do sistema (ALSA)...${NC}"
if command -v aconnect &>/dev/null; then
    echo -e "${BLUE}Portas MIDI detectadas no sistema:${NC}"
    aconnect -l || true
fi

echo -e "\n${GREEN}===================================================================${NC}"
echo -e "${GREEN}  Instalação de Dependências Concluída com Sucesso!                 ${NC}"
echo -e "${GREEN}===================================================================${NC}"
echo -e "Próximos passos:"
echo -e " 1. Configure o Nginx usando os arquivos em ${YELLOW}nginx/${NC}:"
echo -e "    - ${YELLOW}nginx/mixer-site.conf${NC} (site completo) ou"
echo -e "    - ${YELLOW}nginx/mixer.conf${NC} (bloco location para incluir em site existente)"
echo -e " 2. (Opcional) Instale o serviço systemd com ${YELLOW}01v96-controller.service${NC}."
echo -e " 3. Inicie o controlador com: ${YELLOW}./01v96-controller -port <porta_midi>${NC} ou ${YELLOW}./01v96-controller -sim${NC}"
