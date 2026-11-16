dir=/etc/kubernetes/manifests
park=/etc/kubernetes/k8sfront-restart
cri=<% q .CRISocket %>
install -d -m 0700 "$park"
# A previous attempt may have been interrupted with a manifest parked.
for f in "$park"/*.yaml; do
  [ -e "$f" ] || continue
  mv -n "$f" "$dir/"
done
log "kubeadm certs renew all"
kubeadm certs renew all
running() { crictl --runtime-endpoint "$cri" ps -q --state running --name "^$1\$" 2>/dev/null; }
restart() {
  c=$1
  [ -f "$dir/$c.yaml" ] || return 0
  log "Reiniciando $c para carregar os certificados novos"
  mv "$dir/$c.yaml" "$park/"
  for _ in $(seq 1 60); do
    [ -z "$(running "$c")" ] && break
    sleep 2
  done
  mv "$park/$c.yaml" "$dir/"
  for _ in $(seq 1 90); do
    [ -n "$(running "$c")" ] && return 0
    sleep 2
  done
  log "$c não voltou em 3 minutos" >&2
  return 1
}
for c in etcd kube-apiserver kube-controller-manager kube-scheduler kube-vip; do
  restart "$c"
done
log "Aguardando a API responder /readyz"
for _ in $(seq 1 60); do
  if kubectl --kubeconfig /etc/kubernetes/admin.conf get --raw /readyz >/dev/null 2>&1; then
    break
  fi
  sleep 3
done
kubectl --kubeconfig /etc/kubernetes/admin.conf get --raw /readyz >/dev/null
kubeadm certs check-expiration
