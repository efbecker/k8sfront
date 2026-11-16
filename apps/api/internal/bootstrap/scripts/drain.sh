export KUBECONFIG=/etc/kubernetes/admin.conf
node=<% q .Node %>
kubectl cordon "$node"
<% if .Drain -%>
log "Drenando $node (Eviction API: respeita PodDisruptionBudgets, ignora DaemonSets, descarta emptyDir)"
kubectl drain "$node" --ignore-daemonsets --delete-emptydir-data --grace-period=-1 --timeout=<% q .Timeout %>
<% else -%>
log "Cluster de nó único: apenas cordon (não há outro nó para receber os pods)"
<% end -%>
