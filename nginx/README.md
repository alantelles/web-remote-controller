# Configurações Nginx para 01V96 Remote Controller

Este diretório contém as configurações prontas do **Nginx** para expor a aplicação na rota subordinada **`/mixer`**, utilizando **reescrita de URL (`rewrite`)** e **suporte bidirecional completo a WebSocket (`ws://` / `wss://`)**.

---

## Estrutura dos Arquivos

| Arquivo | Descrição |
|---|---|
| `mixer-site.conf` | Virtual Host completo (bloco `server { ... }`) para criar um novo site ou substituir o site padrão em `/etc/nginx/sites-available/`. |
| `mixer.conf` | Trecho modular de rotas (`location /mixer/ { ... }`) projetado para ser incluído dentro de um bloco `server` já existente (`include ...;`). |

---

## Como Funciona a Rota `/mixer` com Rewrite e WebSocket

1. **Redirecionamento Canônico:**
   Acessar `http://seu-servidor/mixer` (sem a barra final) retorna um redirecionamento HTTP 301 para `http://seu-servidor/mixer/`.

2. **Reescrita de URL (`rewrite ^/mixer/(.*)$ /$1 break;`):**
   O backend Go escuta nativamente na raiz (`/`, `/ws`, `/login`, `/m`, `/api/...`).
   Quando o cliente acessa:
   * `/mixer/` $\rightarrow$ é reescrito para `/`
   * `/mixer/ws` $\rightarrow$ é reescrito para `/ws`
   * `/mixer/login` $\rightarrow$ é reescrito para `/login`
   * `/mixer/m` $\rightarrow$ é reescrito para `/m`
   * `/mixer/api/state` $\rightarrow$ é reescrito para `/api/state`

3. **Suporte a WebSocket:**
   * `proxy_http_version 1.1;`
   * `proxy_set_header Upgrade $http_upgrade;`
   * `proxy_set_header Connection $connection_upgrade;`
   * `proxy_read_timeout 86400s;` (garante que a conexão do console não caia por inatividade durante apresentações longas)
   * `proxy_buffering off;` (entrega imediata de mensagens SysEx sem retenção em buffer)

4. **Tratamento de Cookies e Redirecionamentos:**
   * `proxy_redirect / /mixer/;` (transforma redirecionamentos do backend como `/login` em `/mixer/login`)
   * `proxy_cookie_path / /mixer/;` (ajusta o escopo do cookie `remote_session` para a rota `/mixer/`)

---

## Passo a Passo de Instalação (Ubuntu 20.04)

### Opção A: Como Novo Site Dedicado (Recomendado)

```bash
# 1. Copie o arquivo de site para o Nginx
sudo cp nginx/mixer-site.conf /etc/nginx/sites-available/mixer.conf

# 2. Ative o site criando o link simbólico
sudo ln -sf /etc/nginx/sites-available/mixer.conf /etc/nginx/sites-enabled/

# 3. Teste a sintaxe da configuração
sudo nginx -t

# 4. Recarregue o serviço do Nginx
sudo systemctl reload nginx
```

### Opção B: Integrando a um Virtual Host Existente

1. Abra `/etc/nginx/nginx.conf` e certifique-se de que o mapeamento de WebSocket existe dentro do bloco `http { ... }`:
   ```nginx
   http {
       ...
       map $http_upgrade $connection_upgrade {
           default upgrade;
           ''      close;
       }
       ...
   }
   ```

2. No arquivo do seu site em `/etc/nginx/sites-available/seu-site.conf`, dentro do bloco `server { ... }`, adicione:
   ```nginx
   server {
       listen 80;
       server_name seu-dominio.com;

       # Inclui as rotas do mixer
       include /caminho/para/midi-test-go/nginx/mixer.conf;

       # ... outras configurações do seu site ...
   }
   ```

3. Valide e recarregue:
   ```bash
   sudo nginx -t && sudo systemctl reload nginx
   ```
