export KUBECONFIG=/etc/kubernetes/admin.conf
log "Nós do cluster"
kubectl get nodes -o wide
notready=$(kubectl get nodes --no-headers | awk '$2 !~ /^Ready/ {print $1}' | tr '\n' ' ')
if [ -n "${notready// /}" ]; then
  log "Há nós fora do estado Ready: $notready" >&2
  exit 1
fi
pod=$(kubectl -n kube-system get pods -l component=etcd -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
if [ -n "$pod" ]; then
  log "Saúde do etcd (todos os membros)"
  kubectl -n kube-system exec "$pod" -- etcdctl --endpoints=https://127.0.0.1:2379 \
    --cacert=/etc/kubernetes/pki/etcd/ca.crt \
    --cert=/etc/kubernetes/pki/etcd/healthcheck-client.crt \
    --key=/etc/kubernetes/pki/etcd/healthcheck-client.key \
    endpoint health --cluster
fi
log "APIs depreciadas usadas desde o último restart do kube-apiserver"
dep=$(kubectl get --raw /metrics 2>/dev/null | grep '^apiserver_requested_deprecated_apis' || true)
if [ -n "$dep" ]; then
  printf '%s\n' "$dep" | sed -E 's/^apiserver_requested_deprecated_apis\{(.*)\}.*/  aviso: \1/'
else
  log "  nenhuma"
fi
log "PodDisruptionBudgets sem disrupção permitida (podem travar o drain)"
kubectl get pdb -A --no-headers 2>/dev/null | awk '$5 == "0" {print "  aviso: " $1 "/" $2}' || true
