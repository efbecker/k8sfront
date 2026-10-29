log "Baixando imagens do control plane"
kubeadm config images pull --kubernetes-version <% q .Version %> --cri-socket unix:///run/containerd/containerd.sock
