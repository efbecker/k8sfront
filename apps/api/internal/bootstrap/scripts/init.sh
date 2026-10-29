if [ -f /etc/kubernetes/admin.conf ] && [ -f /etc/kubernetes/manifests/kube-apiserver.yaml ]; then
  log "Control plane já inicializado; pulando kubeadm init"
  exit 0
fi
install -d -m 0700 /etc/kubernetes/k8sfront
<% if .KubeVIP -%>
iface=<% q .VIPInterface %>
[ -n "$iface" ] || iface=$(ip -o route show default | awk '{print $5; exit}')
[ -n "$iface" ] || { log "não foi possível detectar a interface para o VIP" >&2; exit 1; }
install -d -m 0755 /etc/kubernetes/manifests
sed "s/__K8SFRONT_VIP_IFACE__/${iface}/" > /etc/kubernetes/manifests/kube-vip.yaml <<'K8SFRONT_EOF'
<% .KubeVIPManifest %>K8SFRONT_EOF
log "kube-vip configurado na interface $iface"
<% end -%>
log "Executando kubeadm init"
# The configuration (with the bootstrap token and certificate key) is passed
# through a pipe, never written to disk or to argv.
kubeadm init --config <(cat <<'K8SFRONT_EOF'
<% .Config %>K8SFRONT_EOF
) --upload-certs
