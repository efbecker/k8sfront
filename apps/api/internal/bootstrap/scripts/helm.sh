export KUBECONFIG=/etc/kubernetes/admin.conf
export HELM_CACHE_HOME=/var/cache/k8sfront/helm HELM_CONFIG_HOME=/etc/k8sfront/helm HELM_DATA_HOME=/var/lib/k8sfront/helm
hv=<% q .HelmVersion %>
case "$(uname -m)" in
  x86_64) arch=amd64 ;;
  aarch64) arch=arm64 ;;
  *) log "arquitetura não suportada pelo Helm" >&2; exit 1 ;;
esac
if ! /usr/local/bin/helm version --short 2>/dev/null | grep -q "^${hv}"; then
  log "Instalando Helm $hv"
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  url="https://get.helm.sh/helm-${hv}-linux-${arch}.tar.gz"
  curl -fsSL --retry 3 -o "$tmp/helm.tar.gz" "$url"
  curl -fsSL --retry 3 -o "$tmp/helm.sha256sum" "$url.sha256sum"
  (cd "$tmp" && printf '%s  helm.tar.gz\n' "$(awk '{print $1}' helm.sha256sum)" | sha256sum --check --status)
  tar -xzf "$tmp/helm.tar.gz" -C "$tmp" "linux-${arch}/helm"
  install -m 0755 "$tmp/linux-${arch}/helm" /usr/local/bin/helm
fi
rel=<% q .Release %>
ns=<% q .Namespace %>
<% if eq .Action "uninstall" -%>
log "helm uninstall $rel -n $ns"
helm uninstall "$rel" -n "$ns" --wait --timeout 10m --ignore-not-found
<% else -%>
args=(upgrade --install "$rel" <% q .Chart %> -n "$ns" --create-namespace --wait --timeout 10m --history-max 10)
<% if .Repo %>args+=(--repo <% q .Repo %>)
<% end -%>
<% if .Version %>args+=(--version <% q .Version %>)
<% end -%>
log "helm upgrade --install $rel <% .Chart %> -n $ns"
helm "${args[@]}" -f <(cat <<'K8SFRONT_EOF'
<% .Values %>
K8SFRONT_EOF
)
helm status "$rel" -n "$ns"
<% end -%>
