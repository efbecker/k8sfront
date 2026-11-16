export KUBECONFIG=/etc/kubernetes/admin.conf
ver=<% q .Version %>
ns=kube-system

# Recém-chegados (workers) só ficam Ready quando o agente da CNI sobe neles.
# Instalar antes disso deixa o pod do metrics-server preso em ContainerCreating.
log "Aguardando todos os nós ficarem Ready (até 10m)"
if ! kubectl wait --for=condition=Ready nodes --all --timeout=10m >/dev/null; then
  log "Nós ainda não Ready:" >&2
  kubectl get nodes -o wide >&2 || true
  exit 1
fi

diagnose() {
  log "---- diagnóstico do metrics-server ----"
  kubectl -n "$ns" get deployment,replicaset,pods -l k8s-app=metrics-server -o wide || true
  kubectl -n "$ns" get events --field-selector involvedObject.kind=Pod --sort-by=.lastTimestamp 2>/dev/null | grep -i metrics-server | tail -n 15 || true
  for p in $(kubectl -n "$ns" get pods -l k8s-app=metrics-server -o name 2>/dev/null); do
    log "-- logs de $p"
    kubectl -n "$ns" logs "$p" --tail=20 2>&1 || true
  done
}

log "Instalando metrics-server $ver"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL --retry 3 --max-time 60 -o "$tmp/components.yaml" \
  "https://github.com/kubernetes-sigs/metrics-server/releases/download/${ver}/components.yaml"
# kubeadm kubelets serve self-signed certificates by default. The flag goes into
# the manifest itself so the Deployment rolls out once (apply + patch created a
# second ReplicaSet and the rollout could wait on the stale pod).
if ! grep -q -- '--kubelet-insecure-tls' "$tmp/components.yaml"; then
  awk '{ print } /^ *- --metric-resolution=/ { sub(/-.*/, ""); print $0 "- --kubelet-insecure-tls" }' \
    "$tmp/components.yaml" >"$tmp/patched.yaml"
  grep -q -- '--kubelet-insecure-tls' "$tmp/patched.yaml" || { log "manifesto do metrics-server em formato inesperado" >&2; exit 1; }
  mv "$tmp/patched.yaml" "$tmp/components.yaml"
fi
kubectl apply -f "$tmp/components.yaml" >/dev/null

log "Aguardando rollout do metrics-server (até 5m)"
if ! kubectl -n "$ns" rollout status deployment/metrics-server --timeout=5m; then
  diagnose
  log "metrics-server não ficou pronto em 5 minutos" >&2
  exit 1
fi
log "metrics-server pronto"
