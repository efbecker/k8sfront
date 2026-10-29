log "Desativando swap (imediato e persistente)"
swapoff -a
if grep -Eq '^[[:space:]]*[^#[:space:]][^[:space:]]*[[:space:]]+[^[:space:]]+[[:space:]]+swap[[:space:]]' /etc/fstab; then
  [ -e /etc/fstab.k8sfront.bak ] || cp -a /etc/fstab /etc/fstab.k8sfront.bak
  sed -ri 's/^([[:space:]]*[^#[:space:]][^[:space:]]*[[:space:]]+[^[:space:]]+[[:space:]]+swap[[:space:]].*)$/#k8sfront# \1/' /etc/fstab
  log "Entradas de swap comentadas em /etc/fstab (backup em /etc/fstab.k8sfront.bak)"
fi
# Debian 13 activates swap through systemd .swap units (fstab generator, zram):
# mask every one so swap does not return on reboot.
for unit in $(systemctl list-units --type=swap --all --plain --no-legend 2>/dev/null | awk '{print $1}'); do
  log "Mascarando unidade $unit"
  systemctl mask --now "$unit" >/dev/null 2>&1 || systemctl mask "$unit" >/dev/null
done
for svc in zramswap.service systemd-zram-setup@zram0.service; do
  if systemctl list-unit-files "$svc" >/dev/null 2>&1 && systemctl is-enabled "$svc" >/dev/null 2>&1; then
    systemctl disable --now "$svc" >/dev/null 2>&1 || true
    systemctl mask "$svc" >/dev/null 2>&1 || true
  fi
done
if [ "$(awk 'NR>1' /proc/swaps | wc -l)" -ne 0 ]; then
  log "swap ainda ativo:" >&2
  cat /proc/swaps >&2
  exit 1
fi
log "Swap desativado"
