if [ -f /etc/kubernetes/kubelet.conf ] || [ -d /etc/kubernetes/manifests ]; then
  log "kubeadm reset"
  kubeadm reset -f --cri-socket <% q .CRISocket %>
else
  log "Nó não tem estado do kubeadm; nada a resetar"
fi
rm -rf /etc/cni/net.d
log "Nó removido do cluster. Pacotes do Kubernetes continuam instalados (em hold); reinicie o servidor para limpar interfaces e regras de rede do CNI."
