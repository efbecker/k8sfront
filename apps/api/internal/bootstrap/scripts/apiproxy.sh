# K8SFRONT_APIPROXY_SYNC
host=<% q .Host %>
port=<% q .Port %>
cfg=/etc/k8sfront/apiproxy.cfg
unit=/etc/systemd/system/k8sfront-apiproxy.service
if ! command -v haproxy >/dev/null 2>&1; then
  log "Instalando HAProxy (pacote do Debian)"
  apt_get update
  apt_get install --no-install-recommends haproxy
fi
# Debian's own haproxy.service has no listener in its default configuration
# and fails to start; it is left alone only when someone configured it.
if ! grep -Eq '^[[:space:]]*(frontend|listen)[[:space:]]' /etc/haproxy/haproxy.cfg 2>/dev/null; then
  if systemctl is-enabled --quiet haproxy.service 2>/dev/null || systemctl is-active --quiet haproxy.service 2>/dev/null; then
    systemctl disable --now haproxy.service >/dev/null 2>&1 || true
    systemctl reset-failed haproxy.service >/dev/null 2>&1 || true
    log "haproxy.service padrão do Debian desativado (sem configuração própria)"
  fi
fi
install -d -m 0755 /etc/k8sfront
tmp=$(mktemp /etc/k8sfront/.apiproxy.XXXXXX)
trap 'rm -f "$tmp"' EXIT
cat > "$tmp" <<'K8SFRONT_EOF'
<% .Config %>K8SFRONT_EOF
haproxy -c -q -f "$tmp"
changed=0
if ! cmp -s "$tmp" "$cfg"; then
  chmod 0644 "$tmp"
  mv -f "$tmp" "$cfg"
  changed=1
fi
cat > "$tmp" <<'K8SFRONT_EOF'
[Unit]
Description=k8sfront: proxy local da API do Kubernetes (HAProxy)
After=network-online.target
Wants=network-online.target
Before=kubelet.service

[Service]
Type=notify
User=haproxy
Group=haproxy
ExecStartPre=/usr/sbin/haproxy -c -q -f /etc/k8sfront/apiproxy.cfg
ExecStart=/usr/sbin/haproxy -Ws -f /etc/k8sfront/apiproxy.cfg
ExecReload=/usr/sbin/haproxy -c -q -f /etc/k8sfront/apiproxy.cfg
ExecReload=/bin/kill -USR2 $MAINPID
Restart=always
RestartSec=2s
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
K8SFRONT_EOF
if ! cmp -s "$tmp" "$unit"; then
  chmod 0644 "$tmp"
  mv -f "$tmp" "$unit"
  systemctl daemon-reload
  changed=1
fi
# The endpoint name always points at the local proxy.
if grep -q '# k8sfront-apiproxy$' /etc/hosts; then
  sed -i "s|^.*# k8sfront-apiproxy\$|127.0.0.1 ${host} # k8sfront-apiproxy|" /etc/hosts
else
  printf '127.0.0.1 %s # k8sfront-apiproxy\n' "$host" >> /etc/hosts
fi
systemctl enable k8sfront-apiproxy.service >/dev/null 2>&1
if systemctl is-active --quiet k8sfront-apiproxy.service; then
  if [ "$changed" = 1 ]; then
    systemctl reload k8sfront-apiproxy.service
    log "Proxy local recarregado"
  fi
else
  systemctl restart k8sfront-apiproxy.service
fi
for _ in $(seq 1 30); do
  if ss -Htln "sport = :${port}" | grep -q "127.0.0.1:${port}"; then
    log "Proxy local da API em 127.0.0.1:${port} (${host}) → <% .Backends %>"
    exit 0
  fi
  sleep 1
done
log "o proxy local não abriu 127.0.0.1:${port}" >&2
systemctl status --no-pager k8sfront-apiproxy.service >&2 || true
exit 1
