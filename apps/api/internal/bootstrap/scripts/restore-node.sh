export KUBECONFIG=/etc/kubernetes/admin.conf
node=<% q .Node %>
ready=$(kubectl get node "$node" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || true)
kubelet=$(kubectl get node "$node" -o jsonpath='{.status.nodeInfo.kubeletVersion}' 2>/dev/null || true)
if [ "$ready" = "True" ]; then
  kubectl uncordon "$node"
  log "$node está Ready (kubelet ${kubelet:-?}): devolvido ao serviço. Retomar faz o drain de novo antes de continuar."
else
  log "$node não está Ready (Ready=${ready:-desconhecido}); continua cordoned para não receber pods."
fi
