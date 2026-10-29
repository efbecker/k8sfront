minor=<% q .Minor %>
log "Configurando repositório oficial pkgs.k8s.io ($minor)"
install -d -m 0755 /etc/apt/keyrings
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
curl -fsSL --retry 3 "https://pkgs.k8s.io/core:/stable:/${minor}/deb/Release.key" -o "$tmp"
gpg --batch --yes --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg "$tmp"
chmod 0644 /etc/apt/keyrings/kubernetes-apt-keyring.gpg
install -m 0644 /dev/stdin /etc/apt/sources.list.d/kubernetes.list <<K8SFRONT_EOF
deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/${minor}/deb/ /
K8SFRONT_EOF
apt_get update
