export KUBECONFIG=/etc/kubernetes/admin.conf
node=<% q .Node %>
label=<% q .Label %>
pod=$(kubectl -n kube-system get pods -l component=etcd --field-selector "spec.nodeName=${node}" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
if [ -z "$pod" ]; then
  log "Nenhum pod de etcd no nó $node (etcd externo?); snapshot não realizado" >&2
  exit 1
fi
name="etcd-$(date -u +%Y%m%dT%H%M%SZ)-${label}.db"
log "Snapshot do etcd via $pod"
kubectl -n kube-system exec "$pod" -- etcdctl --endpoints=https://127.0.0.1:2379 \
  --cacert=/etc/kubernetes/pki/etcd/ca.crt \
  --cert=/etc/kubernetes/pki/etcd/healthcheck-client.crt \
  --key=/etc/kubernetes/pki/etcd/healthcheck-client.key \
  snapshot save /var/lib/etcd/k8sfront-snapshot.db
install -d -m 0700 /var/backups/k8sfront
mv /var/lib/etcd/k8sfront-snapshot.db "/var/backups/k8sfront/$name"
chmod 0600 "/var/backups/k8sfront/$name"
# Keep the 5 most recent snapshots.
ls -1t /var/backups/k8sfront/etcd-*.db 2>/dev/null | tail -n +6 | xargs -r rm -f
log "Snapshot salvo em /var/backups/k8sfront/$name ($(du -h "/var/backups/k8sfront/$name" | cut -f1))"
