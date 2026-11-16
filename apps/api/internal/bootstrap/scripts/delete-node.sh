export KUBECONFIG=/etc/kubernetes/admin.conf
kubectl delete node <% q .Node %> --ignore-not-found
