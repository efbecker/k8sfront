# k8sfront

Plataforma web para **provisionamento, gerenciamento, deploy de aplicações e rolling upgrades** de clusters
Kubernetes sobre **Debian 13 (Trixie) ou superior**.

- `apps/api`: control plane em Go (SQLite WAL, cofre AES-256-GCM, motor SSH + sudo, jobs em background, WebSocket)
- `apps/web`: React + TypeScript + Vite, Tailwind, componentes estilo shadcn/ui, TanStack Query, Xterm.js

## Começando

```bash
make deps
make run            # compila tudo e serve em http://localhost:8080
cat data/admin.token   # token de login gerado na primeira execução
```

Desenvolvimento com hot reload: `make dev-api` e, em outro terminal, `make dev-web` (http://localhost:5173).

Veja [ARCHITECTURE.md](ARCHITECTURE.md) para o desenho completo e [CLAUDE.md](CLAUDE.md) para convenções e regras de segurança.

## Roadmap

1. ✅ Fundação: monorepo, banco, cofre, SSH + sudo com validação de Debian 13+, streaming de logs
2. ✅ Bootstrap com kubeadm + containerd + CNI e Wizard de Deploy
3. client-go, dashboard, nós, workloads, rede, storage, logs e terminal web
4. Central de deploy (formulário visual, Monaco com dry-run, Helm)
5. Rolling upgrades de Kubernetes e do Debian, rolling reboot, circuit breaker, docker-compose
