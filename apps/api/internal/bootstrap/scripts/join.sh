if [ -f /etc/kubernetes/kubelet.conf ]; then
  log "Nó já pertence a um cluster; pulando kubeadm join"
  exit 0
fi
log "Executando kubeadm join<% if .ControlPlane %> (control plane)<% end %>"
kubeadm join --config <(cat <<'K8SFRONT_EOF'
<% .Config %>K8SFRONT_EOF
)
<% if .KubeVIP -%>
iface=<% q .VIPInterface %>
[ -n "$iface" ] || iface=$(ip -o route show default | awk '{print $5; exit}')
sed "s/__K8SFRONT_VIP_IFACE__/${iface}/" > /etc/kubernetes/manifests/kube-vip.yaml <<'K8SFRONT_EOF'
<% .KubeVIPManifest %>K8SFRONT_EOF
log "kube-vip configurado na interface $iface"
<% end -%>
