log "Aguardando o kubelet subir (até 5m)"
for _ in $(seq 1 100); do
  if systemctl is-active --quiet kubelet; then
    log "kubelet ativo"
    exit 0
  fi
  sleep 3
done
systemctl status kubelet --no-pager >&2 || true
log "kubelet não ficou ativo em 5 minutos" >&2
exit 1
