log "Instalando containerd e dependências"
apt_get update
apt_get install --no-install-recommends containerd runc ca-certificates curl gpg conntrack socat ethtool iptables
install -d -m 0755 /etc/containerd
cfg=/etc/containerd/config.toml
if ! grep -q '^# k8sfront-managed' "$cfg" 2>/dev/null; then
  [ ! -s "$cfg" ] || cp -a "$cfg" "$cfg.k8sfront.bak"
  { echo '# k8sfront-managed'; containerd config default; } > "$cfg.new"
  mv "$cfg.new" "$cfg"
  log "Gerado $cfg a partir de 'containerd config default'"
fi
sed -ri 's/^([[:space:]]*)SystemdCgroup[[:space:]]*=[[:space:]]*false/\1SystemdCgroup = true/' "$cfg"
if ! grep -Eq '^[[:space:]]*SystemdCgroup[[:space:]]*=[[:space:]]*true' "$cfg"; then
  log "Não foi possível ativar SystemdCgroup em $cfg" >&2
  exit 1
fi
log "SystemdCgroup = true"
install -m 0644 /dev/stdin /etc/crictl.yaml <<'K8SFRONT_EOF'
runtime-endpoint: unix:///run/containerd/containerd.sock
image-endpoint: unix:///run/containerd/containerd.sock
timeout: 10
K8SFRONT_EOF
systemctl enable containerd >/dev/null
systemctl restart containerd
systemctl is-active --quiet containerd
log "containerd $(containerd --version | awk '{print $3}') ativo"
