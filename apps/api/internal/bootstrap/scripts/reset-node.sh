if [ -f /etc/kubernetes/kubelet.conf ] || [ -d /etc/kubernetes/manifests ]; then
  log "kubeadm reset"
  kubeadm reset -f --cri-socket <% q .CRISocket %>
else
  log "Nó não tem estado do kubeadm; nada a resetar"
fi
rm -rf /etc/cni/net.d
log "Nó removido do cluster. Pacotes do Kubernetes continuam instalados (em hold); reinicie o servidor para limpar interfaces e regras de rede do CNI."
# Local API proxy (after kubeadm reset, which may still need the API).
if [ -f /etc/systemd/system/k8sfront-apiproxy.service ]; then
  systemctl disable --now k8sfront-apiproxy.service >/dev/null 2>&1 || true
  rm -f /etc/systemd/system/k8sfront-apiproxy.service /etc/k8sfront/apiproxy.cfg
  systemctl daemon-reload
  log "Proxy local da API removido"
fi
sed -i '/# k8sfront-apiproxy$/d' /etc/hosts
