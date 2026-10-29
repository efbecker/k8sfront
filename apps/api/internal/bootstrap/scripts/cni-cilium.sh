export KUBECONFIG=/etc/kubernetes/admin.conf
ver=<% q .Version %>
cli=<% q .CLIVersion %>
case "$(uname -m)" in
  x86_64) arch=amd64 ;;
  aarch64) arch=arm64 ;;
  *) log "arquitetura não suportada pelo cilium-cli" >&2; exit 1 ;;
esac
if ! /usr/local/bin/cilium version --client 2>/dev/null | grep -q "${cli#v}"; then
  log "Instalando cilium-cli $cli"
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  url="https://github.com/cilium/cilium-cli/releases/download/${cli}/cilium-linux-${arch}.tar.gz"
  curl -fsSL --retry 3 -o "$tmp/cilium.tar.gz" "$url"
  curl -fsSL --retry 3 -o "$tmp/cilium.tar.gz.sha256sum" "$url.sha256sum"
  (cd "$tmp" && sed 's#cilium-linux-.*\.tar\.gz#cilium.tar.gz#' cilium.tar.gz.sha256sum | sha256sum --check --status)
  tar -xzf "$tmp/cilium.tar.gz" -C /usr/local/bin cilium
fi
if kubectl -n kube-system get daemonset cilium >/dev/null 2>&1; then
  log "Cilium já instalado"
else
  log "Instalando Cilium $ver"
  cilium install --version "$ver" --set ipam.mode=kubernetes
fi
log "Aguardando Cilium ficar pronto"
cilium status --wait --wait-duration 10m
