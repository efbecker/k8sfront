log "Configurando módulos do kernel e sysctl"
install -m 0644 /dev/stdin /etc/modules-load.d/k8s.conf <<'K8SFRONT_EOF'
overlay
br_netfilter
K8SFRONT_EOF
modprobe overlay
modprobe br_netfilter
install -m 0644 /dev/stdin /etc/sysctl.d/k8s.conf <<'K8SFRONT_EOF'
net.bridge.bridge-nf-call-iptables  = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.ipv4.ip_forward                 = 1
K8SFRONT_EOF
sysctl --system >/dev/null
for key in net.bridge.bridge-nf-call-iptables net.bridge.bridge-nf-call-ip6tables net.ipv4.ip_forward; do
  val=$(sysctl -n "$key")
  [ "$val" = "1" ] || { log "$key = $val (esperado 1)" >&2; exit 1; }
  log "$key = 1"
done
