export KUBECONFIG=/etc/kubernetes/admin.conf
ver=<% q .Version %>
ns=metallb-system

log "Aguardando todos os nós ficarem Ready (até 10m)"
if ! kubectl wait --for=condition=Ready nodes --all --timeout=10m >/dev/null; then
  kubectl get nodes -o wide >&2 || true
  log "Nós ainda não Ready" >&2
  exit 1
fi

# kube-proxy em modo IPVS responde ARP por IPs de Services em todas as
# interfaces; o MetalLB L2 exige strictARP nesse caso.
mode=$(kubectl -n kube-system get configmap kube-proxy -o jsonpath='{.data.config\.conf}' 2>/dev/null | awk '$1=="mode:"{gsub(/"/,"",$2); print $2}')
if [ "$mode" = "ipvs" ]; then
  if kubectl -n kube-system get configmap kube-proxy -o jsonpath='{.data.config\.conf}' | grep -q 'strictARP: false'; then
    log "kube-proxy em IPVS: ativando strictARP"
    kubectl -n kube-system get configmap kube-proxy -o yaml | sed -e 's/strictARP: false/strictARP: true/' | kubectl apply -f - >/dev/null
    kubectl -n kube-system rollout restart daemonset/kube-proxy >/dev/null
  fi
fi

diagnose() {
  log "---- diagnóstico do MetalLB ----"
  kubectl -n "$ns" get deployment,daemonset,pods -o wide || true
  kubectl -n "$ns" get events --sort-by=.lastTimestamp 2>/dev/null | tail -n 15 || true
}

log "Instalando MetalLB $ver (modo L2)"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL --retry 3 --max-time 60 -o "$tmp/metallb.yaml" \
  "https://raw.githubusercontent.com/metallb/metallb/${ver}/config/manifests/metallb-native.yaml"
kubectl apply --server-side --force-conflicts -f "$tmp/metallb.yaml" >/dev/null

log "Aguardando controller e speakers do MetalLB (até 5m)"
if ! kubectl -n "$ns" rollout status deployment/controller --timeout=5m || ! kubectl -n "$ns" rollout status daemonset/speaker --timeout=5m; then
  diagnose
  log "MetalLB não ficou pronto em 5 minutos" >&2
  exit 1
fi

# kubeadm marca os control planes com exclude-from-external-load-balancers.
# Se todos os nós têm o rótulo (cluster de um nó ou só de control planes),
# nenhum speaker anunciaria os IPs: o rótulo sai desses nós.
total=$(kubectl get nodes -o name | wc -l)
excluded=$(kubectl get nodes -l node.kubernetes.io/exclude-from-external-load-balancers -o name | wc -l)
if [ "$total" -gt 0 ] && [ "$excluded" -eq "$total" ]; then
  log "Todos os nós estão excluídos de load balancers externos: removendo o rótulo para o MetalLB anunciar os IPs"
  kubectl label nodes --all node.kubernetes.io/exclude-from-external-load-balancers- >/dev/null
fi

cat >"$tmp/pool.yaml" <<'K8SFRONT_EOF'
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata:
  name: k8sfront
  namespace: metallb-system
spec:
  addresses:
<%- range .Addresses %>
  - <% . %>
<%- end %>
---
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata:
  name: k8sfront
  namespace: metallb-system
spec:
  ipAddressPools:
  - k8sfront
K8SFRONT_EOF

# O webhook de validação do MetalLB leva alguns segundos para aceitar pedidos.
log "Aplicando a faixa de IPs: <% range $i, $a := .Addresses %><% if $i %>, <% end %><% $a %><% end %>"
for i in $(seq 1 30); do
  if kubectl apply -f "$tmp/pool.yaml" >/dev/null 2>"$tmp/err"; then
    log "MetalLB pronto: Services LoadBalancer recebem IPs da faixa"
    exit 0
  fi
  sleep 5
done
cat "$tmp/err" >&2
diagnose
log "não foi possível aplicar o IPAddressPool do MetalLB" >&2
exit 1
