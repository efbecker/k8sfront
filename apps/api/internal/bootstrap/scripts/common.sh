set -euo pipefail
apt_get() {
  DEBIAN_FRONTEND=noninteractive apt-get -y -q \
    -o DPkg::Lock::Timeout=300 \
    -o Dpkg::Options::="--force-confdef" \
    -o Dpkg::Options::="--force-confold" "$@"
}
log() { printf '%s\n' "$*"; }
