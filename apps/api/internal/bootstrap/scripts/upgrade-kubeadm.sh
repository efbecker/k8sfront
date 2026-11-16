want=<% q .Plain %>
ver=<% q .Version %>
pkgver=$(apt-cache madison kubeadm | awk -v v="$want" '$3 ~ "^"v"-" {print $3; exit}')
if [ -z "$pkgver" ]; then
  log "kubeadm $want não encontrado no repositório configurado" >&2
  exit 1
fi
cur=$(kubeadm version -o short 2>/dev/null || true)
if [ "$cur" != "$ver" ]; then
  log "Atualizando kubeadm ${cur:-?} → $pkgver"
  apt-mark unhold kubeadm >/dev/null
  apt_get install --allow-change-held-packages kubeadm="$pkgver"
  apt-mark hold kubeadm >/dev/null
fi
log "kubeadm $(kubeadm version -o short)"
<% if .Apply -%>
export KUBECONFIG=/etc/kubernetes/admin.conf
server=$(kubectl get --raw /version | sed -n 's/.*"gitVersion": *"\([^"]*\)".*/\1/p')
if [ "$server" = "$ver" ]; then
  log "Control plane já está em $ver; pulando kubeadm upgrade apply"
else
  log "kubeadm upgrade plan $ver"
  kubeadm upgrade plan "$ver" || log "aviso: kubeadm upgrade plan retornou erro (detalhes acima)"
  log "kubeadm upgrade apply $ver (control plane ${server:-?} → $ver)"
  kubeadm upgrade apply -y "$ver"
fi
<% else -%>
log "kubeadm upgrade node"
kubeadm upgrade node
<% end -%>
