# k8sfront

Plataforma web para **provisionamento, gerenciamento, deploy de aplicações e rolling upgrades** de clusters
Kubernetes sobre **Debian 13 (Trixie) ou superior**.

- `apps/api`: control plane em Go (SQLite WAL, cofre AES-256-GCM, motor SSH + sudo, jobs em background, WebSocket)
- `apps/web`: React + TypeScript + Vite, Tailwind, componentes estilo shadcn/ui, TanStack Query, Xterm.js

## Instalação num servidor Debian 13+

```bash
curl -fsSL https://raw.githubusercontent.com/efbecker/k8sfront/main/install.sh | sudo bash
sudo cat /var/lib/k8sfront/admin.token   # token de login
```

O instalador valida Debian 13+, instala em `/opt/k8sfront`, guarda dados em `/var/lib/k8sfront` e cria o serviço
systemd `k8sfront` (usuário dedicado, sandbox do systemd). Usa a release publicada mais recente (tarball com sha256);
se ainda não houver release, compila do código-fonte (Go baixado de go.dev com sha256 + Node.js do Debian).
Rodar de novo atualiza no lugar. Opções: `K8SFRONT_PORT`, `K8SFRONT_LISTEN`, `K8SFRONT_VERSION` (tag ou `main`),
`K8SFRONT_SOURCE=auto|release|source`, ex.: `curl -fsSL ... | sudo K8SFRONT_PORT=9090 bash`.

Releases: `git tag v0.2.0 && git push origin v0.2.0` dispara o workflow que publica os tarballs (`make dist`).

## Desenvolvimento

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
