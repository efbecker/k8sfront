want=<% q .Plain %>
pkgver=$(apt-cache madison kubeadm | awk -v v="$want" '$3 ~ "^"v"-" {print $3; exit}')
if [ -z "$pkgver" ]; then
  log "kubeadm $want não encontrado no repositório configurado" >&2
  exit 1
fi
log "Instalando kubelet, kubeadm e kubectl $pkgver"
apt-mark unhold kubelet kubeadm kubectl >/dev/null 2>&1 || true
apt_get install --allow-change-held-packages kubelet="$pkgver" kubeadm="$pkgver" kubectl="$pkgver" cri-tools
apt-mark hold kubelet kubeadm kubectl
systemctl enable kubelet >/dev/null
# Align the containerd sandbox (pause) image with what this kubeadm expects.
pause=$(kubeadm config images list --kubernetes-version <% q .Version %> 2>/dev/null | grep '/pause:' | head -n1 || true)
cfg=/etc/containerd/config.toml
if [ -n "$pause" ] && ! grep -Eq "^[[:space:]]*sandbox(_image)?[[:space:]]*=[[:space:]]*[\"']${pause}[\"']" "$cfg"; then
  sed -ri "s#^([[:space:]]*sandbox(_image)?[[:space:]]*=[[:space:]]*).*#\1\"${pause}\"#" "$cfg"
  systemctl restart containerd
  log "Imagem sandbox do containerd: $pause"
fi
log "Pacotes em hold: $(apt-mark showhold | tr '\n' ' ')"
