#!/usr/bin/env bash
#
#
# Qué hace:
#   1. Pregunta dominio, nombre del sitio, ruta de instalación, credenciales de BD
#   2. Instala Go, PostgreSQL y Nginx (si no están ya instalados)
#   3. Crea la base de datos y el usuario de Postgres
#   4. Copia el código, sustituye los placeholders (__DOMAIN__, __SITE_NAME__)
#   5. Compila el binario
#   6. Crea el servicio systemd (con ADMIN_TOKEN autogenerado)
#   7. Configura Nginx (estáticos + proxy a la API y al announce)
#
# Uso:
#   sudo ./install.sh
#   (o sin interacción: sudo ./install.sh --domain=ejemplo.com --site-name="Mi Tracker")
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVICE_NAME="tracker"

# ============================================================
# COLORES (solo para que el instalador se lea mejor, opcional)
# ============================================================
BOLD='\033[1m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

info()    { echo -e "${GREEN}→${NC} $1"; }
warn()    { echo -e "${YELLOW}⚠️  $1${NC}"; }
error()   { echo -e "${RED}❌ $1${NC}" >&2; }
success() { echo -e "${GREEN}✓${NC} $1"; }

# ============================================================
# ARGUMENTOS OPCIONALES (para instalación no interactiva)
# ============================================================
ARG_DOMAIN=""
ARG_SITE_NAME=""
ARG_INSTALL_DIR=""
ARG_DB_PASSWORD=""
ARG_ADMIN_TOKEN=""

for arg in "$@"; do
  case "$arg" in
    --domain=*)      ARG_DOMAIN="${arg#*=}" ;;
    --site-name=*)   ARG_SITE_NAME="${arg#*=}" ;;
    --install-dir=*) ARG_INSTALL_DIR="${arg#*=}" ;;
    --db-password=*) ARG_DB_PASSWORD="${arg#*=}" ;;
    --admin-token=*) ARG_ADMIN_TOKEN="${arg#*=}" ;;
    --help|-h)
      echo "Uso: sudo ./install.sh [--domain=ejemplo.com] [--site-name=\"Mi Tracker\"]"
      echo "                       [--install-dir=/var/www/tracker] [--db-password=...] [--admin-token=...]"
      echo "Sin argumentos, el instalador te pregunta cada valor de forma interactiva."
      exit 0
      ;;
  esac
done

# ============================================================
# COMPROBACIONES PREVIAS
# ============================================================
if [[ $EUID -ne 0 ]]; then
  error "Este script debe ejecutarse como root (usa: sudo ./install.sh)"
  exit 1
fi

if [[ ! -f "$SCRIPT_DIR/main.go" ]]; then
  error "No encuentro main.go en $SCRIPT_DIR"
  echo "   Ejecuta este script desde dentro de la carpeta descomprimida del proyecto." >&2
  exit 1
fi

echo ""
echo -e "${BOLD}=========================================================="
echo "  Instalador de la plantilla de tracker BitTorrent + web"
echo -e "==========================================================${NC}"
echo ""

# ============================================================
# PREGUNTAS INTERACTIVAS (si no se pasaron por argumento)
# ============================================================

ask() {
  # ask "pregunta" "valor_por_defecto" -> escribe la respuesta en $REPLY_VALUE
  local prompt="$1"
  local default="${2:-}"
  local input
  if [[ -n "$default" ]]; then
    read -rp "$(echo -e "${BOLD}${prompt}${NC} [${default}]: ")" input
    REPLY_VALUE="${input:-$default}"
  else
    read -rp "$(echo -e "${BOLD}${prompt}${NC}: ")" input
    REPLY_VALUE="$input"
  fi
}

ask_secret() {
  # igual que ask, pero sin mostrar lo que se escribe (para contraseñas)
  local prompt="$1"
  local input
  read -rsp "$(echo -e "${BOLD}${prompt}${NC}: ")" input
  echo ""
  REPLY_VALUE="$input"
}

# --- Dominio ---
if [[ -n "$ARG_DOMAIN" ]]; then
  DOMAIN="$ARG_DOMAIN"
else
  ask "¿Cuál es el dominio público de tu tracker? (ej. tracker.ejemplo.com)" ""
  while [[ -z "$REPLY_VALUE" ]]; do
    warn "El dominio no puede estar vacío."
    ask "¿Cuál es el dominio público de tu tracker?" ""
  done
  DOMAIN="$REPLY_VALUE"
fi

# --- Nombre del sitio ---
if [[ -n "$ARG_SITE_NAME" ]]; then
  SITE_NAME="$ARG_SITE_NAME"
else
  ask "¿Qué nombre quieres mostrar en la web?" "MyTracker"
  SITE_NAME="$REPLY_VALUE"
fi

# --- Carpeta de instalación ---
if [[ -n "$ARG_INSTALL_DIR" ]]; then
  INSTALL_DIR="$ARG_INSTALL_DIR"
else
  ask "¿En qué carpeta quieres instalar el proyecto?" "/var/www/tracker"
  INSTALL_DIR="$REPLY_VALUE"
fi

# --- Nombre de la base de datos / usuario ---
ask "Nombre de la base de datos Postgres" "tracker"
DB_NAME="$REPLY_VALUE"

ask "Usuario de Postgres para la app" "tracker_user"
DB_USER="$REPLY_VALUE"

# --- Contraseña de la base de datos ---
if [[ -n "$ARG_DB_PASSWORD" ]]; then
  DB_PASSWORD="$ARG_DB_PASSWORD"
else
  echo ""
  echo "Contraseña de la base de datos:"
  echo "  - Déjalo en blanco para que se genere una automáticamente (recomendado)"
  ask_secret "Contraseña (o Enter para autogenerar)"
  if [[ -z "$REPLY_VALUE" ]]; then
    DB_PASSWORD=$(openssl rand -base64 24 | tr -dc 'a-zA-Z0-9' | head -c 32)
    info "Contraseña autogenerada para la base de datos."
  else
    DB_PASSWORD="$REPLY_VALUE"
    # Avisamos si tiene caracteres que rompen la URL de conexión postgres://
    if [[ "$DB_PASSWORD" =~ [^a-zA-Z0-9] ]]; then
      warn "Tu contraseña tiene caracteres especiales. Si la conexión a la BD falla,"
      warn "usa solo letras y números, o codifícala en formato URL manualmente."
    fi
  fi
fi

# --- Admin token ---
if [[ -n "$ARG_ADMIN_TOKEN" ]]; then
  ADMIN_TOKEN="$ARG_ADMIN_TOKEN"
else
  ADMIN_TOKEN=$(openssl rand -hex 32)
  info "ADMIN_TOKEN autogenerado para el panel de administración."
fi

echo ""
echo -e "${BOLD}Resumen de la instalación:${NC}"
echo "  Dominio:           $DOMAIN"
echo "  Nombre del sitio:  $SITE_NAME"
echo "  Carpeta:           $INSTALL_DIR"
echo "  Base de datos:     $DB_NAME (usuario: $DB_USER)"
echo ""
read -rp "¿Continuar con la instalación? [s/N]: " CONFIRM
if [[ ! "$CONFIRM" =~ ^[sSyY]$ ]]; then
  echo "Instalación cancelada."
  exit 0
fi
echo ""

# ============================================================
# 1. PAQUETES DEL SISTEMA
# ============================================================
info "Actualizando lista de paquetes..."
apt-get update -qq

install_if_missing() {
  local pkg="$1"
  if ! dpkg -l "$pkg" &>/dev/null; then
    info "Instalando $pkg..."
    apt-get install -y -qq "$pkg"
  else
    success "$pkg ya está instalado"
  fi
}

install_if_missing sudo
install_if_missing postgresql
install_if_missing nginx
install_if_missing curl
install_if_missing openssl

# --- Go: instalamos la versión oficial (no la de apt, suele ir muy atrasada) ---
GO_VERSION="1.22.5"
if command -v go &>/dev/null && go version 2>/dev/null | grep -q "go${GO_VERSION}"; then
  success "Go ${GO_VERSION} ya está instalado"
else
  info "Instalando Go ${GO_VERSION}..."
  ARCH=$(dpkg --print-architecture)
  case "$ARCH" in
    amd64) GO_ARCH="amd64" ;;
    arm64) GO_ARCH="arm64" ;;
    *) error "Arquitectura no soportada por este script: $ARCH"; exit 1 ;;
  esac
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${GO_ARCH}.tar.gz" -o /tmp/go.tar.gz
  rm -rf /usr/local/go
  tar -C /usr/local -xzf /tmp/go.tar.gz
  rm /tmp/go.tar.gz
  ln -sf /usr/local/go/bin/go /usr/local/bin/go
  ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
fi

echo ""
success "Go instalado: $(go version)"
echo ""

# ============================================================
# 2. BASE DE DATOS POSTGRESQL
# ============================================================
info "Configurando PostgreSQL..."

systemctl enable --now postgresql

# Usamos 'runuser' en vez de 'sudo -u postgres' para no depender de que el
# paquete 'sudo' esté instalado (algunas imágenes mínimas de Debian no lo
# traen de fábrica).
pg() { runuser -u postgres -- psql "$@"; }

DB_EXISTS=$(pg -tAc "SELECT 1 FROM pg_database WHERE datname='${DB_NAME}'") || true

if [[ "$DB_EXISTS" == "1" ]]; then
  info "La base de datos '${DB_NAME}' ya existe. Se mantienen sus datos."
else
  info "Creando la base de datos '${DB_NAME}'..."
  pg -c "CREATE DATABASE ${DB_NAME};"
fi

# IMPORTANTE: esto se ejecuta SIEMPRE, exista o no la base de datos de antes.
# Así, si vuelves a ejecutar el instalador (por ejemplo tras un fallo a medias
# en una ejecución anterior), la contraseña de Postgres y la que se guarda en
# el servicio systemd SIEMPRE quedan sincronizadas. Sin esto, un segundo
# intento podía generar una contraseña nueva sin actualizar Postgres,
# dejando el servicio sin poder conectar nunca.
pg <<EOF
DO \$\$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = '${DB_USER}') THEN
    CREATE USER ${DB_USER} WITH PASSWORD '${DB_PASSWORD}';
  ELSE
    ALTER USER ${DB_USER} WITH PASSWORD '${DB_PASSWORD}';
  END IF;
END
\$\$;
GRANT ALL PRIVILEGES ON DATABASE ${DB_NAME} TO ${DB_USER};
EOF

pg -d "${DB_NAME}" <<EOF
GRANT ALL ON SCHEMA public TO ${DB_USER};
ALTER SCHEMA public OWNER TO ${DB_USER};
EOF

success "Base de datos '${DB_NAME}' y usuario '${DB_USER}' sincronizados (contraseña incluida)."

# ============================================================
# 3. DESPLIEGUE DEL CÓDIGO (con sustitución de placeholders)
# ============================================================
echo ""
info "Desplegando código en ${INSTALL_DIR}..."

mkdir -p "${INSTALL_DIR}/public/torrents"

cp "$SCRIPT_DIR"/*.go "${INSTALL_DIR}/"
cp "$SCRIPT_DIR"/go.mod "${INSTALL_DIR}/"
[[ -f "$SCRIPT_DIR/go.sum" ]] && cp "$SCRIPT_DIR/go.sum" "${INSTALL_DIR}/"
cp "$SCRIPT_DIR"/schema.sql "${INSTALL_DIR}/" 2>/dev/null || true
cp "$SCRIPT_DIR"/public/*.html "${INSTALL_DIR}/public/"
cp "$SCRIPT_DIR"/public/app.js "${INSTALL_DIR}/public/"
cp "$SCRIPT_DIR"/public/style.css "${INSTALL_DIR}/public/"

# Sustituir placeholders __DOMAIN__ y __SITE_NAME__ en todos los archivos copiados
find "${INSTALL_DIR}" -type f \( -name "*.go" -o -name "*.html" -o -name "*.js" \) -print0 | \
  xargs -0 sed -i "s|__DOMAIN__|${DOMAIN}|g; s|__SITE_NAME__|${SITE_NAME}|g"

success "Archivos copiados y placeholders sustituidos (__DOMAIN__, __SITE_NAME__)."

# ============================================================
# 4. COMPILACIÓN
# ============================================================
echo ""
info "Compilando el binario..."
cd "${INSTALL_DIR}"
export PATH="/usr/local/go/bin:$PATH"
go mod tidy
go build -o http-tracker .
success "Compilado: ${INSTALL_DIR}/http-tracker"

# ============================================================
# 5. SERVICIO SYSTEMD
# ============================================================
echo ""
info "Configurando el servicio systemd..."

SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"

if [[ -f "$SERVICE_FILE" ]]; then
  warn "Ya existe ${SERVICE_FILE}. Se sobrescribirá con los nuevos datos."
fi

cat > "$SERVICE_FILE" <<EOF
[Unit]
Description=${SITE_NAME} - tracker BitTorrent
After=network.target postgresql.service
Requires=postgresql.service

[Service]
Environment="ADMIN_TOKEN=${ADMIN_TOKEN}"
Environment="DATABASE_URL=postgres://${DB_USER}:${DB_PASSWORD}@localhost:5432/${DB_NAME}?sslmode=disable"
Type=simple
User=root
Restart=always
RestartSec=5
ExecStart=${INSTALL_DIR}/http-tracker
WorkingDirectory=${INSTALL_DIR}

[Install]
WantedBy=multi-user.target
EOF

success "Servicio creado en ${SERVICE_FILE}"

systemctl daemon-reload
systemctl enable "${SERVICE_NAME}"
systemctl restart "${SERVICE_NAME}"

sleep 2
if systemctl is-active --quiet "${SERVICE_NAME}"; then
  success "Servicio '${SERVICE_NAME}' arrancado correctamente."
else
  warn "El servicio no parece estar activo. Revisa con:"
  echo "    journalctl -u ${SERVICE_NAME} -n 50 --no-pager"
fi

# ============================================================
# 6. NGINX
# ============================================================
echo ""
info "Configurando Nginx..."

cat > /etc/nginx/sites-available/default <<EOF
server {
    listen 80 default_server;
    listen [::]:80 default_server;
    server_name ${DOMAIN};

    client_max_body_size 20M;

    root ${INSTALL_DIR}/public;
    index index.html;

    gzip on;
    gzip_types text/css application/javascript application/json text/html;
    gzip_min_length 256;

    location / {
        try_files \$uri \$uri/ =404;
    }

    location ~* \.(css|js)\$ {
        expires 7d;
        add_header Cache-Control "public";
    }

    location /api/ {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
    }

    location /announce {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
    }
}
EOF

rm -f /etc/nginx/sites-enabled/default
ln -sf /etc/nginx/sites-available/default /etc/nginx/sites-enabled/default

if nginx -t &>/tmp/nginx-test.log; then
  systemctl enable nginx
  systemctl reload nginx 2>/dev/null || systemctl restart nginx
  success "Nginx configurado y recargado."
else
  error "Error en la configuración de Nginx:"
  cat /tmp/nginx-test.log
  exit 1
fi

# ============================================================
# RESUMEN FINAL
# ============================================================
echo ""
echo -e "${BOLD}=========================================================="
echo "  INSTALACIÓN COMPLETA"
echo -e "==========================================================${NC}"
echo ""
echo "Sitio:                  ${SITE_NAME}"
echo "Dominio (Nginx):         ${DOMAIN}"
echo "Carpeta del proyecto:    ${INSTALL_DIR}"
echo "Carpeta web (estáticos): ${INSTALL_DIR}/public"
echo ""
echo -e "${BOLD}🔑 ADMIN_TOKEN (panel /admin.html):${NC}"
echo "   ${ADMIN_TOKEN}"
echo ""
echo -e "${BOLD}🗄️  Base de datos:${NC}"
echo "   Nombre BD: ${DB_NAME}"
echo "   Usuario:   ${DB_USER}"
echo "   Password:  ${DB_PASSWORD}"
echo ""
warn "GUARDA ESTOS DATOS AHORA — no se repiten en pantalla."
echo "   También quedan en ${SERVICE_FILE}"
echo ""
echo -e "${BOLD}IMPORTANTE — revisa tú mismo:${NC}"
echo "  • Si pusiste un dominio de prueba o quieres cambiarlo más adelante,"
echo "    edita TrackerAnnounceURL en ${INSTALL_DIR}/main.go y recompila:"
echo "      cd ${INSTALL_DIR} && go build -o http-tracker . && systemctl restart ${SERVICE_NAME}"
echo "  • Si usas Caddy u otro proxy delante de Nginx (para HTTPS), apunta ese"
echo "    proxy hacia el puerto 80 de esta máquina, NO hacia el 8080."
echo ""
echo "Comandos útiles:"
echo "  systemctl status ${SERVICE_NAME}"
echo "  journalctl -u ${SERVICE_NAME} -f"
echo "  systemctl status nginx"
echo ""
