set -euo pipefail
apt_get_once() {
  DEBIAN_FRONTEND=noninteractive apt-get -y -q \
    -o DPkg::Lock::Timeout=300 \
    -o Acquire::Retries=3 \
    -o Dpkg::Options::="--force-confdef" \
    -o Dpkg::Options::="--force-confold" "$@"
}
# apt_get retries once after repairing the usual transient causes of exit
# 100: an interrupted dpkg, stale package lists or a failed download.
apt_get() {
  local rc=0
  apt_get_once "$@" || rc=$?
  [ "$rc" -eq 0 ] && return 0
  log "apt-get $1 falhou (status $rc); reparando dpkg, atualizando as listas e tentando de novo"
  DEBIAN_FRONTEND=noninteractive dpkg --configure -a --force-confdef --force-confold || true
  if [ "$1" != "update" ]; then
    apt_get_once update || true
  fi
  apt_get_once "$@"
}
log() { printf '%s\n' "$*"; }
