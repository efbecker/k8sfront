export KUBECONFIG=/etc/kubernetes/admin.conf
ver=<% q .Version %>
log "Instalando metrics-server $ver"
kubectl apply -f "https://github.com/kubernetes-sigs/metrics-server/releases/download/${ver}/components.yaml" >/dev/null
# kubeadm kubelets serve self-signed certificates by default.
if ! kubectl -n kube-system get deployment metrics-server -o jsonpath='{.spec.template.spec.containers[0].args}' | grep -q -- '--kubelet-insecure-tls'; then
  kubectl -n kube-system patch deployment metrics-server --type=json \
    -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]' >/dev/null
fi
kubectl -n kube-system rollout status deployment/metrics-server --timeout=5m
