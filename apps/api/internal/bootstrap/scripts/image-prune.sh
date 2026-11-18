# K8SFRONT_IMAGE_PRUNE: remove images not used by any container on this node.
before=$(crictl images -q | sort -u | wc -l)
used_before=$(df -B1 --output=used /var/lib/containerd 2>/dev/null | tail -n 1 | tr -d ' ' || echo 0)
log "Imagens antes: $before"
crictl rmi --prune
after=$(crictl images -q | sort -u | wc -l)
used_after=$(df -B1 --output=used /var/lib/containerd 2>/dev/null | tail -n 1 | tr -d ' ' || echo 0)
freed=$(( used_before > used_after ? used_before - used_after : 0 ))
log "Imagens depois: $after ($(( before - after )) removida(s), ~$(( freed / 1048576 )) MiB liberados)"
echo "K8SFRONT_PRUNE removed=$(( before - after )) remaining=$after freed=$freed"
