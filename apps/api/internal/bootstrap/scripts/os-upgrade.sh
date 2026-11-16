log "Atualizando pacotes do sistema (apt-get upgrade; pacotes em hold são preservados)"
apt_get update
before=$(apt-mark showhold | sort | tr '\n' ' ')
apt_get upgrade --with-new-pkgs
after=$(apt-mark showhold | sort | tr '\n' ' ')
if [ "$before" != "$after" ]; then
  log "Os pacotes em hold mudaram durante o upgrade ($before → $after)" >&2
  exit 1
fi
log "Pacotes em hold preservados: $after"
if [ -f /var/run/reboot-required ]; then
  log "Reinício necessário: $(tr '\n' ' ' < /var/run/reboot-required.pkgs 2>/dev/null || true)"
  echo "K8SFRONT_REBOOT=yes"
else
  echo "K8SFRONT_REBOOT=no"
fi
