#!/usr/bin/env bash
# k8sfront installer for Debian 13 (Trixie) or newer.
#
#   curl -fsSL https://raw.githubusercontent.com/efbecker/k8sfront/main/install.sh | sudo bash
#
# Re-running it upgrades the installation in place (data in /var/lib/k8sfront is kept).
#
# Options (environment variables, e.g. `| sudo K8SFRONT_PORT=9090 bash`):
#   K8SFRONT_VERSION   release tag (v0.2.0), "latest" (default) or a git ref such as "main"
#   K8SFRONT_SOURCE    auto (default: release if one exists, else build from source) | release | source
#   K8SFRONT_PORT      HTTP port (default 8080)
#   K8SFRONT_LISTEN    bind address (default 0.0.0.0)
#   K8SFRONT_REPO      GitHub repository (default efbecker/k8sfront)
set -Eeuo pipefail

REPO="${K8SFRONT_REPO:-efbecker/k8sfront}"
VERSION="${K8SFRONT_VERSION:-latest}"
SOURCE="${K8SFRONT_SOURCE:-auto}"
PORT="${K8SFRONT_PORT:-8080}"
LISTEN="${K8SFRONT_LISTEN:-0.0.0.0}"

PREFIX=/opt/k8sfront
DATA_DIR=/var/lib/k8sfront
CONF_DIR=/etc/k8sfront
ENV_FILE="$CONF_DIR/k8sfront.env"
UNIT=/etc/systemd/system/k8sfront.service
SVC_USER=k8sfront
NODE_MIN=20.19.0

export DEBIAN_FRONTEND=noninteractive LC_ALL=C.UTF-8
APT=(apt-get -y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold)

if [ -t 1 ]; then B=$'\e[1m'; G=$'\e[32m'; Y=$'\e[33m'; R=$'\e[31m'; N=$'\e[0m'; else B= G= Y= R= N=; fi
log()  { printf '%s==>%s %s\n' "$B$G" "$N" "$*"; }
warn() { printf '%s!!%s  %s\n' "$B$Y" "$N" "$*" >&2; }
die()  { trap - ERR; printf '%sERRO:%s %s\n' "$B$R" "$N" "$*" >&2; exit 1; }
trap 'die "falhou na linha $LINENO: $BASH_COMMAND"' ERR

WORK=""
cleanup() { [ -n "$WORK" ] && rm -rf "$WORK"; }
trap cleanup EXIT

# --- pre-checks ---------------------------------------------------------------

[ "$(id -u)" -eq 0 ] || die "execute como root (ex.: curl -fsSL ... | sudo bash)"

[ -r /etc/os-release ] || die "/etc/os-release não encontrado"
# Read in a subshell: os-release defines VERSION, which would clobber ours.
OS_ID="$(. /etc/os-release && echo "${ID:-}")"
VERSION_ID="$(. /etc/os-release && echo "${VERSION_ID:-}")"
[ "$OS_ID" = debian ] || die "o k8sfront exige Debian 13 (Trixie) ou superior; encontrado ID=${OS_ID:-?}"
case "$VERSION_ID" in
  '' ) die "VERSION_ID ausente em /etc/os-release (testing/sid não são suportados)";;
  *[!0-9.]* ) die "VERSION_ID inválido: $VERSION_ID";;
esac
[ "${VERSION_ID%%.*}" -ge 13 ] || die "o k8sfront exige Debian 13 (Trixie) ou superior; encontrado Debian $VERSION_ID"

command -v systemctl >/dev/null && [ -d /run/systemd/system ] || die "systemd não está ativo neste host"

case "$(dpkg --print-architecture)" in
  amd64) ARCH=amd64;;
  arm64) ARCH=arm64;;
  *) die "arquitetura não suportada: $(dpkg --print-architecture) (use amd64 ou arm64)";;
esac

case "$PORT" in ''|*[!0-9]*) die "K8SFRONT_PORT inválida: $PORT";; esac
case "$SOURCE" in auto|release|source) ;; *) die "K8SFRONT_SOURCE deve ser auto, release ou source";; esac

log "Debian $VERSION_ID ($ARCH) detectado"

log "Instalando dependências básicas"
"${APT[@]}" update -qq
"${APT[@]}" install -qq --no-install-recommends ca-certificates curl tar jq >/dev/null

WORK="$(mktemp -d)"
STAGE="$WORK/stage"
mkdir -p "$STAGE/bin" "$STAGE/web"

gh_api() { curl -fsSL -H 'Accept: application/vnd.github+json' "https://api.github.com/repos/$REPO/$1"; }

# --- prebuilt release ----------------------------------------------------------

# Only looks the release up; returns 1 when there is none (no side effects).
find_release() {
  local tag json asset url sums
  if [ "$VERSION" = latest ]; then
    json="$(gh_api releases/latest 2>/dev/null)" || return 1
  else
    json="$(gh_api "releases/tags/$VERSION" 2>/dev/null)" || return 1
  fi
  tag="$(jq -r .tag_name <<<"$json")"
  asset="k8sfront_${tag}_linux_${ARCH}.tar.gz"
  url="$(jq -r --arg n "$asset" '.assets[] | select(.name==$n) | .browser_download_url' <<<"$json")"
  sums="$(jq -r '.assets[] | select(.name=="SHA256SUMS") | .browser_download_url' <<<"$json")"
  [ -n "$url" ] && [ -n "$sums" ] || { warn "release $tag não tem $asset/SHA256SUMS"; return 1; }

  RELEASE_TAG="$tag" RELEASE_ASSET="$asset" RELEASE_URL="$url" RELEASE_SUMS="$sums"
}

# Runs outside any `if`/`||` so that `set -e` applies to every command.
install_release() {
  log "Baixando release $RELEASE_TAG"
  curl -fsSL -o "$WORK/$RELEASE_ASSET" "$RELEASE_URL"
  curl -fsSL -o "$WORK/SHA256SUMS" "$RELEASE_SUMS"
  (cd "$WORK" && grep " $RELEASE_ASSET\$" SHA256SUMS | sha256sum -c --quiet -) ||
    die "checksum inválido para $RELEASE_ASSET"
  tar -xzf "$WORK/$RELEASE_ASSET" -C "$WORK"
  cp "$WORK/k8sfront/bin/k8sfront-api" "$STAGE/bin/"
  cp -r "$WORK/k8sfront/web/." "$STAGE/web/"
  INSTALLED_VERSION="$RELEASE_TAG"
}

# --- build from source ---------------------------------------------------------

version_ge() { [ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -n1)" = "$2" ]; }

ensure_go() {
  local want have line ver file sha
  want="$(sed -n 's/^go \([0-9.]*\).*/\1/p' "$SRC/apps/api/go.mod")"
  want="${want%.*}" # 1.26.0 -> 1.26
  if command -v go >/dev/null; then
    have="$(go env GOVERSION | sed 's/^go//')"
    if version_ge "$have" "$want"; then GO=go; return; fi
  fi
  log "Baixando Go $want (go.dev, verificado por sha256)"
  line="$(curl -fsSL 'https://go.dev/dl/?mode=json&include=all' | jq -r --arg w "go$want" --arg a "$ARCH" '
      [ .[] | select(.stable and (.version == $w or (.version | startswith($w + "."))))
            | .files[] | select(.os == "linux" and .arch == $a and .kind == "archive") ]
      | first | "\(.version) \(.filename) \(.sha256)"')"
  read -r ver file sha <<<"$line"
  [ -n "${sha:-}" ] && [ "$sha" != null ] || die "não encontrei Go $want para linux/$ARCH em go.dev"
  curl -fsSL -o "$WORK/$file" "https://go.dev/dl/$file"
  echo "$sha  $WORK/$file" | sha256sum -c --quiet - || die "checksum inválido para $file"
  tar -xzf "$WORK/$file" -C "$WORK"
  GO="$WORK/go/bin/go"
  log "Usando $ver"
}

ensure_node() {
  if ! command -v node >/dev/null || ! command -v npm >/dev/null; then
    log "Instalando Node.js e npm (pacotes Debian)"
    "${APT[@]}" install -qq --no-install-recommends nodejs npm >/dev/null
  fi
  local have
  have="$(node -p 'process.versions.node')"
  version_ge "$have" "$NODE_MIN" || die "Node.js $have é antigo; o build exige >= $NODE_MIN"
}

build_source() {
  local ref="$VERSION"
  [ "$ref" = latest ] && ref=main
  log "Compilando a partir do código-fonte ($REPO@$ref)"
  "${APT[@]}" install -qq --no-install-recommends git >/dev/null
  SRC="$WORK/src"
  if ! git clone -q --depth 1 --branch "$ref" "https://github.com/$REPO.git" "$SRC" 2>/dev/null; then
    rm -rf "$SRC"
    git clone -q "https://github.com/$REPO.git" "$SRC"
    git -C "$SRC" checkout -q "$ref" || die "ref '$ref' não existe em $REPO"
  fi
  ensure_go
  ensure_node
  INSTALLED_VERSION="$(git -C "$SRC" describe --tags --always 2>/dev/null || echo "$ref")"

  log "Build do frontend (npm ci + vite build)"
  (cd "$SRC" && npm ci --no-audit --no-fund --loglevel=error && npm run build -w apps/web --silent) >/dev/null
  log "Build do control plane (Go, CGO desabilitado)"
  (cd "$SRC/apps/api" && GOTOOLCHAIN=local GOFLAGS=-mod=readonly GOCACHE="$WORK/gocache" GOPATH="$WORK/gopath" \
    CGO_ENABLED=0 "$GO" build -trimpath \
    -ldflags "-s -w -X github.com/efbecker/k8sfront/apps/api/internal/api.Version=$INSTALLED_VERSION" \
    -o "$STAGE/bin/k8sfront-api" ./cmd/k8sfront-api)
  cp -r "$SRC/apps/web/dist/." "$STAGE/web/"
}

INSTALLED_VERSION=""
MODE=source
if [ "$SOURCE" != source ]; then
  if find_release; then
    MODE=release
  elif [ "$SOURCE" = release ]; then
    die "release '$VERSION' não encontrada em github.com/$REPO/releases"
  else
    warn "nenhuma release publicada para '$VERSION'; compilando do código-fonte"
  fi
fi
if [ "$MODE" = release ]; then install_release; else build_source; fi

# --- install ---------------------------------------------------------------------

log "Instalando em $PREFIX"
id -u "$SVC_USER" >/dev/null 2>&1 ||
  useradd --system --home-dir "$DATA_DIR" --no-create-home --shell /usr/sbin/nologin "$SVC_USER"

UPGRADE=0
systemctl is-active --quiet k8sfront 2>/dev/null && UPGRADE=1

install -d -m 0755 "$PREFIX" "$PREFIX/bin"
install -m 0755 "$STAGE/bin/k8sfront-api" "$PREFIX/bin/k8sfront-api.new"
rm -rf "$PREFIX/web.new" && cp -r "$STAGE/web" "$PREFIX/web.new"
chmod -R u=rwX,go=rX "$PREFIX/web.new"
mv -f "$PREFIX/bin/k8sfront-api.new" "$PREFIX/bin/k8sfront-api"
rm -rf "$PREFIX/web.old"
if [ -d "$PREFIX/web" ]; then mv "$PREFIX/web" "$PREFIX/web.old"; fi
mv "$PREFIX/web.new" "$PREFIX/web"
rm -rf "$PREFIX/web.old"
echo "$INSTALLED_VERSION" > "$PREFIX/VERSION"

install -d -m 0700 -o "$SVC_USER" -g "$SVC_USER" "$DATA_DIR"
install -d -m 0750 -o root -g "$SVC_USER" "$CONF_DIR"
if [ ! -f "$ENV_FILE" ]; then
  # Secrets (master key, admin token) are generated by the service itself inside $DATA_DIR (0600).
  cat > "$ENV_FILE" <<EOF
# k8sfront: variáveis documentadas em apps/api/internal/config/config.go
K8SFRONT_ADDR=$LISTEN:$PORT
K8SFRONT_DATA_DIR=$DATA_DIR
K8SFRONT_WEB_DIR=$PREFIX/web
K8SFRONT_LOG_LEVEL=info
# Atrás de um proxy HTTPS, habilite:
#K8SFRONT_SECURE_COOKIES=true
#K8SFRONT_STRICT_HOST_KEYS=true
EOF
  chown root:"$SVC_USER" "$ENV_FILE"; chmod 0640 "$ENV_FILE"
else
  log "Mantendo configuração existente em $ENV_FILE"
fi

cat > "$UNIT" <<EOF
[Unit]
Description=k8sfront - Kubernetes on Debian 13+
Documentation=https://github.com/$REPO
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
User=$SVC_USER
Group=$SVC_USER
EnvironmentFile=$ENV_FILE
ExecStart=$PREFIX/bin/k8sfront-api
Restart=on-failure
RestartSec=5
UMask=0077
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
ReadWritePaths=$DATA_DIR
CapabilityBoundingSet=
AmbientCapabilities=

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
if [ "$UPGRADE" = 1 ]; then
  log "Reiniciando serviço (upgrade)"
  systemctl restart k8sfront
else
  systemctl enable --now k8sfront >/dev/null 2>&1
fi

# --- wait and report -------------------------------------------------------------

port_now="$(sed -n 's/^K8SFRONT_ADDR=.*:\([0-9]*\)$/\1/p' "$ENV_FILE")"; port_now="${port_now:-$PORT}"
for _ in $(seq 1 30); do
  curl -fsS -o /dev/null "http://127.0.0.1:$port_now/api/v1/health" 2>/dev/null && break
  sleep 1
done
curl -fsS -o /dev/null "http://127.0.0.1:$port_now/api/v1/health" ||
  die "o serviço não respondeu; veja: journalctl -u k8sfront -n 50"

ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
echo
log "k8sfront $INSTALLED_VERSION em execução"
echo "    URL:          http://${ip:-<ip-do-servidor>}:$port_now"
if [ -f "$DATA_DIR/admin.token" ] && ! grep -q '^K8SFRONT_ADMIN_TOKEN=' "$ENV_FILE"; then
  echo "    Token admin:  sudo cat $DATA_DIR/admin.token"
fi
echo "    Config:       $ENV_FILE"
echo "    Dados:        $DATA_DIR  (faça backup de master.key: sem ela os segredos não podem ser decifrados)"
echo "    Logs:         journalctl -u k8sfront -f"
echo "    Atualizar:    rode este instalador novamente"
echo
echo "    Em produção, coloque o k8sfront atrás de HTTPS (ex.: Caddy/nginx) e defina K8SFRONT_SECURE_COOKIES=true."
