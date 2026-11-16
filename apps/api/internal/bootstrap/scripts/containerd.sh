log "Instalando containerd e dependências"
apt_get update
apt_get install --no-install-recommends containerd runc ca-certificates curl gpg conntrack socat ethtool iptables containernetworking-plugins
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
# Debian's containerd looks for CNI plugins in /usr/lib/cni, but Cilium and
# Calico install theirs into /opt/cni/bin (upstream default). Point the CRI
# plugin there and seed it with the standard plugins (loopback etc.) shipped
# by containernetworking-plugins, without overwriting what a CNI installed.
sed -ri -e 's#^([[:space:]]*)bin_dir[[:space:]]*=.*#\1bin_dir = "/opt/cni/bin"#' \
  -e "s#^([[:space:]]*)bin_dirs[[:space:]]*=.*#\\1bin_dirs = ['/opt/cni/bin']#" "$cfg"
if ! grep -Eq '^[[:space:]]*bin_dirs?[[:space:]]*=.*/opt/cni/bin' "$cfg"; then
  log "Não foi possível apontar o diretório de plugins CNI para /opt/cni/bin em $cfg" >&2
  exit 1
fi
install -d -m 0755 /opt/cni/bin /etc/cni/net.d
for f in /usr/lib/cni/*; do
  if [ -f "$f" ] && [ ! -e "/opt/cni/bin/${f##*/}" ]; then cp -p "$f" /opt/cni/bin/; fi
done
[ -x /opt/cni/bin/loopback ] || { log "plugin CNI loopback ausente em /opt/cni/bin" >&2; exit 1; }
log "Plugins CNI em /opt/cni/bin"
install -m 0644 /dev/stdin /etc/crictl.yaml <<'K8SFRONT_EOF'
runtime-endpoint: unix:///run/containerd/containerd.sock
image-endpoint: unix:///run/containerd/containerd.sock
timeout: 10
K8SFRONT_EOF
systemctl enable containerd >/dev/null
systemctl restart containerd
systemctl is-active --quiet containerd
log "containerd $(containerd --version | awk '{print $3}') ativo"
