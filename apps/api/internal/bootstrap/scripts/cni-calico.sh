export KUBECONFIG=/etc/kubernetes/admin.conf
ver=<% q .Version %>
base="https://raw.githubusercontent.com/projectcalico/calico/${ver}/manifests"
log "Instalando Calico $ver (tigera-operator)"
kubectl apply --server-side --force-conflicts -f "$base/operator-crds.yaml" >/dev/null
kubectl apply --server-side --force-conflicts -f "$base/tigera-operator.yaml" >/dev/null
kubectl wait --for=condition=Established --timeout=180s crd/installations.operator.tigera.io crd/apiservers.operator.tigera.io >/dev/null
kubectl apply -f - <<'K8SFRONT_EOF'
apiVersion: operator.tigera.io/v1
kind: Installation
metadata:
  name: default
spec:
  calicoNetwork:
    ipPools:
    - name: default-ipv4-ippool
      blockSize: 26
      cidr: <% .PodCIDR %>
      encapsulation: VXLANCrossSubnet
      natOutgoing: Enabled
      nodeSelector: all()
---
apiVersion: operator.tigera.io/v1
kind: APIServer
metadata:
  name: default
spec: {}
K8SFRONT_EOF
log "Aguardando Calico ficar disponível"
for i in $(seq 1 60); do
  if kubectl get tigerastatus calico -o jsonpath='{.status.conditions[?(@.type=="Available")].status}' 2>/dev/null | grep -q True; then
    log "Calico disponível"
    exit 0
  fi
  sleep 10
done
log "Calico não ficou disponível em 10 minutos" >&2
kubectl get tigerastatus >&2 || true
exit 1
