log "Atualizando índices do APT"
apt_get update >/dev/null
apt list --upgradable 2>/dev/null | grep 'upgradable from' | sed 's/^/K8SFRONT_PKG /' || true
apt-mark showhold | sed 's/^/K8SFRONT_HOLD /'
if [ -f /var/run/reboot-required ]; then echo "K8SFRONT_REBOOT=yes"; else echo "K8SFRONT_REBOOT=no"; fi
echo "K8SFRONT_KERNEL=$(uname -r)"
