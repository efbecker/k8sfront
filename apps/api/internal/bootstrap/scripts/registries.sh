# K8SFRONT_REGISTRIES_SYNC: containerd registry hosts (hosts.toml) on this node.
d=<% q .CertsDir %>
cfg=<% q .Config %>
install -d -m 0755 "$d"
restart=0
# containerd 1.7 (config v2) ships config_path = "" and 2.x already points at
# certs.d (possibly followed by :/etc/docker/certs.d).
has_config_path() { grep -Eq "^[[:space:]]*config_path[[:space:]]*=[[:space:]]*[\"']${d}[\"':]" "$cfg"; }
if [ -f "$cfg" ] && ! has_config_path; then
  cp -a "$cfg" "$cfg.k8sfront-registries.bak"
  sed -ri "s#^([[:space:]]*)config_path[[:space:]]*=[[:space:]]*(\"\"|'')[[:space:]]*\$#\\1config_path = '${d}'#" "$cfg"
  if ! has_config_path; then
    log "config_path do registry não encontrado em $cfg (configure-o manualmente apontando para $d)" >&2
    exit 1
  fi
  log "containerd passa a ler $d (config_path)"
  restart=1
fi
want=()
<% range .Hosts -%>
dir="$d"/<% q .Dir %>
want+=(<% q .Dir %>)
install -d -m 0755 "$dir"
<% if .CA -%>
install -m 0644 /dev/stdin "$dir/ca.crt" <<'K8SFRONT_EOF'
<% .CA %>K8SFRONT_EOF
<% else -%>
rm -f "$dir/ca.crt"
<% end -%>
install -m 0644 /dev/stdin "$dir/hosts.toml.new" <<'K8SFRONT_EOF'
<% .TOML %>K8SFRONT_EOF
if cmp -s "$dir/hosts.toml.new" "$dir/hosts.toml"; then
  rm -f "$dir/hosts.toml.new"
  log "  $dir: sem mudança"
else
  mv "$dir/hosts.toml.new" "$dir/hosts.toml"
  log "  $dir: hosts.toml atualizado"
fi
: > "$dir/.k8sfront-managed"
<% end -%>
for dir in "$d"/*/; do
  [ -f "${dir}.k8sfront-managed" ] || continue
  name=$(basename "$dir")
  keep=0
  for w in "${want[@]}"; do
    if [ "$w" = "$name" ]; then keep=1; fi
  done
  if [ "$keep" = 0 ]; then
    rm -rf -- "${dir%/}"
    log "  $d/$name: removido (registry não existe mais)"
  fi
done
if [ "$restart" = 1 ]; then
  log "Reiniciando containerd (os containers em execução continuam rodando)"
  systemctl restart containerd
  for _ in $(seq 1 60); do
    if crictl info >/dev/null 2>&1; then break; fi
    sleep 1
  done
  crictl info >/dev/null
fi
log "${#want[@]} registry(s) configurado(s) no containerd"
