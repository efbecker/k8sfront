m=/etc/kubernetes/manifests/kube-vip.yaml
if [ -f "$m" ] && grep -q 'super-admin.conf' "$m"; then
  sed -i 's#/etc/kubernetes/super-admin.conf#/etc/kubernetes/admin.conf#' "$m"
  log "kube-vip passou a usar admin.conf"
fi
