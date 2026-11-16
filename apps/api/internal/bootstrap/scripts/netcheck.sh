export KUBECONFIG=/etc/kubernetes/admin.conf
ns=kube-system
ds=k8sfront-netcheck

log "Aguardando todos os nós ficarem Ready (até 10m)"
if ! kubectl wait --for=condition=Ready nodes --all --timeout=10m >/dev/null; then
  kubectl get nodes -o wide >&2 || true
  log "Nem todos os nós ficaram Ready em 10 minutos" >&2
  exit 1
fi

# Ready only means the CNI wrote its config. A tiny DaemonSet proves that the
# runtime can actually create pod sandboxes with networking on every node
# (catches e.g. containerd looking for CNI plugins in the wrong directory).
pause=$(kubeadm config images list --kubernetes-version <% q .Version %> 2>/dev/null | grep '/pause:' | head -n1 || true)
[ -n "$pause" ] || pause=registry.k8s.io/pause:3.10
log "Verificando rede de pods em todos os nós"
kubectl -n "$ns" delete daemonset "$ds" --ignore-not-found --wait=true >/dev/null
kubectl apply -f - >/dev/null <<K8SFRONT_EOF
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: ${ds}
  namespace: ${ns}
  labels: {app: ${ds}}
spec:
  selector:
    matchLabels: {app: ${ds}}
  template:
    metadata:
      labels: {app: ${ds}}
    spec:
      tolerations: [{operator: Exists}]
      terminationGracePeriodSeconds: 0
      containers:
      - name: pause
        image: ${pause}
        resources:
          requests: {cpu: 1m, memory: 4Mi}
K8SFRONT_EOF
if ! kubectl -n "$ns" rollout status "daemonset/$ds" --timeout=5m; then
  log "---- diagnóstico da rede de pods ----"
  kubectl -n "$ns" get pods -l "app=$ds" -o wide || true
  kubectl -n "$ns" get events --field-selector reason=FailedCreatePodSandBox --sort-by=.lastTimestamp 2>/dev/null | tail -n 10 || true
  kubectl -n "$ns" delete daemonset "$ds" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  log "Pods não conseguem subir com rede em todos os nós (veja os eventos acima)" >&2
  exit 1
fi
kubectl -n "$ns" get pods -l "app=$ds" -o wide
kubectl -n "$ns" delete daemonset "$ds" --wait=false >/dev/null

log "Aguardando CoreDNS (até 5m)"
kubectl -n "$ns" rollout status deployment/coredns --timeout=5m
log "Rede de pods OK em todos os nós"
