export KUBECONFIG=/etc/kubernetes/admin.conf
node=<% q .Node %>
peer=<% q .PeerURL %>
# Any etcd pod that is not on the node being removed.
pod=$(kubectl -n kube-system get pods -l component=etcd -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.spec.nodeName}{"\n"}{end}' |
  awk -v n="$node" '$2 != n && $1 != "" {print $1; exit}')
if [ -z "$pod" ]; then
  log "Nenhum pod de etcd em outro control plane; não é possível conferir os membros do etcd" >&2
  exit 1
fi
ectl() {
  kubectl -n kube-system exec "$pod" -- etcdctl --endpoints=https://127.0.0.1:2379 \
    --cacert=/etc/kubernetes/pki/etcd/ca.crt \
    --cert=/etc/kubernetes/pki/etcd/healthcheck-client.crt \
    --key=/etc/kubernetes/pki/etcd/healthcheck-client.key "$@"
}
ids=$(ectl member list | awk -F', ' -v n="$node" -v p="$peer" '(n != "" && $3 == n) || index($4, p) {print $1}')
if [ -z "$ids" ]; then
  log "O etcd já não tem membro para ${node:-$peer}"
else
  for id in $ids; do
    log "Removendo membro $id do etcd"
    ectl member remove "$id"
  done
fi
log "Membros do etcd:"
ectl member list
