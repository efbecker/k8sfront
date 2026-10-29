export KUBECONFIG=/etc/kubernetes/admin.conf
log "Removendo taint do control plane para permitir workloads"
for n in $(kubectl get nodes -l node-role.kubernetes.io/control-plane -o name); do
  kubectl taint "$n" node-role.kubernetes.io/control-plane:NoSchedule- >/dev/null 2>&1 || true
done
kubectl get nodes -o custom-columns=NODE:.metadata.name,TAINTS:.spec.taints[*].key
