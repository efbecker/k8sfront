want=<% q .Plain %>
pkgver=$(apt-cache madison kubelet | awk -v v="$want" '$3 ~ "^"v"-" {print $3; exit}')
if [ -z "$pkgver" ]; then
  log "kubelet $want não encontrado no repositório configurado" >&2
  exit 1
fi
log "Atualizando kubelet e kubectl para $pkgver"
apt-mark unhold kubelet kubectl >/dev/null
apt_get install --allow-change-held-packages kubelet="$pkgver" kubectl="$pkgver"
apt-mark hold kubelet kubectl >/dev/null
# Keep the containerd sandbox (pause) image aligned with the new kubeadm.
pause=$(kubeadm config images list --kubernetes-version <% q .Version %> 2>/dev/null | grep '/pause:' | head -n1 || true)
cfg=/etc/containerd/config.toml
if [ -n "$pause" ] && [ -f "$cfg" ] && ! grep -Eq "^[[:space:]]*sandbox(_image)?[[:space:]]*=[[:space:]]*[\"']${pause}[\"']" "$cfg"; then
  sed -ri "s#^([[:space:]]*sandbox(_image)?[[:space:]]*=[[:space:]]*).*#\1\"${pause}\"#" "$cfg"
  systemctl restart containerd
  log "Imagem sandbox do containerd: $pause"
fi
systemctl daemon-reload
systemctl restart kubelet
log "$(kubelet --version); pacotes em hold: $(apt-mark showhold | tr '\n' ' ')"
