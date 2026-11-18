set -euo pipefail
apt_get_once() {
  DEBIAN_FRONTEND=noninteractive apt-get -y -q \
    -o DPkg::Lock::Timeout=300 \
    -o Acquire::Retries=3 \
    -o Dpkg::Options::="--force-confdef" \
    -o Dpkg::Options::="--force-confold" "$@"
}
# apt_busy lists the processes that keep the APT/dpkg locks (apt-daily,
# unattended-upgrades or a manual apt on a freshly installed server).
apt_busy() {
  ps -eo pid=,etimes=,comm=,args= 2>/dev/null |
    awk '$3 ~ /^(apt|apt-get|aptitude|dpkg|unattended-upgr|apt.systemd.dai|apt-check)$/' || true
}
# apt_wait_idle waits up to 10 minutes for those to finish.
apt_wait_idle() {
  local i
  for i in $(seq 1 60); do
    [ -z "$(apt_busy)" ] && return 0
    [ "$i" -eq 1 ] && log "aguardando outro processo do apt/dpkg terminar (ex.: unattended-upgrades):" && apt_busy | sed 's/^/  /'
    sleep 10
  done
  log "o apt/dpkg continua ocupado após 10 minutos"
}
# apt_diag prints what usually explains an exit 100.
apt_diag() {
  log "--- diagnóstico do apt ---"
  df -h / /var 2>/dev/null | sed 's/^/  /' || true
  local busy
  busy=$(apt_busy)
  if [ -n "$busy" ]; then log "processos do apt/dpkg em execução:"; printf '%s\n' "$busy" | sed 's/^/  /'; fi
  log "pacotes em hold: $(apt-mark showhold 2>/dev/null | tr '\n' ' ')"
  log "repositórios do Kubernetes: $(grep -rhs 'pkgs.k8s.io' /etc/apt/sources.list /etc/apt/sources.list.d/ | tr '\n' ' ')"
  dpkg --audit 2>/dev/null | sed 's/^/  /' || true
}
# apt_get retries once after repairing the usual transient causes of exit
# 100: another apt holding the lock, an interrupted dpkg, stale package lists,
# a full package cache or a failed download. A second failure prints a
# diagnosis next to apt's own "E:" lines.
apt_get() {
  local rc=0
  apt_get_once "$@" || rc=$?
  [ "$rc" -eq 0 ] && return 0
  log "apt-get $1 falhou (status $rc); reparando dpkg, limpando o cache, atualizando as listas e tentando de novo"
  apt_wait_idle
  DEBIAN_FRONTEND=noninteractive dpkg --configure -a --force-confdef --force-confold || true
  apt-get clean || true
  if [ "$1" != "update" ]; then
    apt_get_once update || true
  fi
  rc=0
  apt_get_once "$@" || rc=$?
  [ "$rc" -eq 0 ] && return 0
  apt_diag
  return "$rc"
}
log() { printf '%s\n' "$*"; }
