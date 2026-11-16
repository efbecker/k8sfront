export KUBECONFIG=/etc/kubernetes/admin.conf
node=<% q .Node %>
want=<% q .Version %>
log "Aguardando $node Ready (até 10m)"
deadline=$((SECONDS + 600))
while :; do
  ready=$(kubectl get node "$node" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || true)
  v=$(kubectl get node "$node" -o jsonpath='{.status.nodeInfo.kubeletVersion}' 2>/dev/null || true)
  if [ "$ready" = "True" ] && { [ -z "$want" ] || [ "$v" = "$want" ]; }; then
    break
  fi
  if [ "$SECONDS" -ge "$deadline" ]; then
    kubectl get node "$node" -o wide >&2 || true
    log "$node não ficou Ready${want:+ com kubelet $want} em 10 minutos (Ready=$ready, kubelet=$v)" >&2
    exit 1
  fi
  sleep 5
done
log "$node Ready (kubelet $v)"
