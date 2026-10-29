export KUBECONFIG=/etc/kubernetes/admin.conf
cfg() { cat <<'K8SFRONT_EOF'
<% .Config %>K8SFRONT_EOF
}
log "Gerando token de join (TTL 2h)"
kubeadm token delete <% q .TokenID %> >/dev/null 2>&1 || true
kubeadm token create --config <(cfg) >/dev/null
<% if .UploadCerts -%>
log "Publicando certificados do control plane (chave temporária)"
kubeadm init phase upload-certs --upload-certs --config <(cfg) >/dev/null
<% end -%>
log "Credenciais de join prontas"
