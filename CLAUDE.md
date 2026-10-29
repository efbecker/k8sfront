# CLAUDE.md

Guia para agentes (e humanos) trabalhando no **k8sfront**: plataforma web para bootstrap, gestão diária,
deploy de aplicações e rolling upgrades de clusters Kubernetes rodando **exclusivamente em Debian 13 (Trixie) ou superior**.

Leia `ARCHITECTURE.md` antes de mudanças estruturais. Este arquivo traz as regras do dia a dia.

## Layout

```
apps/api   Control plane em Go (módulo github.com/efbecker/k8sfront/apps/api)
apps/web   Frontend React + TypeScript + Vite + Tailwind v4 + componentes estilo shadcn/ui
```

## Comandos

| Tarefa | Comando |
| --- | --- |
| Instalar dependências | `make deps` |
| API em modo dev (:8080, dados em `./data`) | `make dev-api` |
| Frontend em modo dev (:5173, proxy de `/api`) | `make dev-web` |
| Build completo | `make build` |
| Servir SPA pela API | `make run` |
| Testes unitários (Go com `-race` + Vitest) | `make test` |
| Integração SSH/sudo real (precisa root; Docker opcional) | `make test-integration` |
| gofmt + vet + typecheck | `make lint` |
| Tarballs de release + SHA256SUMS em `dist/` | `make dist VERSION=vX.Y.Z` |

Instalação em servidor: `install.sh` (curl | sudo bash; release com sha256 ou build do fonte, serviço systemd). Teste mudanças nele num container `debian:trixie`.
Login local: o token de admin vem de `K8SFRONT_ADMIN_TOKEN` ou é gerado em `data/admin.token` na primeira execução.
Variáveis de ambiente estão documentadas em `apps/api/internal/config/config.go`.

## Convenções Go (`apps/api`)

- Go 1.26, apenas stdlib + dependências já presentes (`x/crypto/ssh`, `modernc.org/sqlite`, `coder/websocket`, `google/uuid`; `client-go` entra na Fase 3). Sem CGO.
- Roteamento com `net/http.ServeMux` (padrões `"GET /api/v1/x/{id}"`). Logs estruturados com `log/slog`.
- Persistência só através da interface `store.Store`. SQL específico de SQLite fica em `internal/store/sqlite`; nada fora desse pacote importa o driver. Novas tabelas = novo arquivo `migrations/NNNN_nome.sql` (nunca edite uma migration já publicada).
- Operações longas são **jobs** (`internal/jobs`), nunca trabalho dentro do handler HTTP. Handlers de job ficam em `internal/ops`, são idempotentes e usam `jc.StepOnce` para etapas que não devem repetir num retry.
- Erros de validação → 400 com `{"error":"validation","message":...}`; use `store.ErrNotFound`/`store.ErrConflict` para 404/409.
- Scripts de bootstrap ficam em `internal/bootstrap/scripts/*.sh` (text/template com delimitadores `<% %>`, use `q` para quoting). Todo script novo precisa passar no `bash -n` do teste e não pode conter `apt-key` nem segredos em linha de comando; segredos de kubeadm entram por process substitution.
- Versões de componentes (Cilium, Calico, kube-vip, metrics-server) são fixadas em `internal/bootstrap/spec.go`; atualize lá e rode `K8SFRONT_KUBEADM=/caminho/kubeadm go test ./internal/bootstrap/` quando mexer nas configs do kubeadm.
- `gofmt` obrigatório; testes ao lado do código (`*_test.go`), integração com build tag `integration` em `test/integration`.

## Convenções Web (`apps/web`)

- Componentes UI em `src/components/ui` seguem a API do shadcn/ui (cva + `cn`). Ícones só de `lucide-react`.
- Dados do servidor via TanStack Query (`src/lib/api.ts` é o único lugar que chama `fetch`). Streams via `JobStream` (`src/lib/jobStream.ts`), que retoma pelo último `seq`.
- Logs de execução sempre em Xterm.js (`LogConsole`). Edição de YAML (Fase 4) será Monaco.
- Textos de interface em português; identificadores e código em inglês. Suporte obrigatório a tema claro e escuro.

## Regras de segurança (não negociáveis)

1. **Debian 13+ antes de tudo.** Toda conexão a um servidor passa por `remote.Connector.Open`, que lê `/etc/os-release` e aborta se `ID != debian` ou `VERSION_ID < 13` antes de qualquer outro comando. Não crie atalhos que pulem essa validação.
2. **Senha do sudo só via stdin.** Use `sshx.Client.Run(..., RunOptions{Sudo: true})`. Ele executa `sudo -k -S -p '' -- /bin/bash -c '<wrapper fixo>'` e envia a senha e depois o script pelo stdin. Nunca coloque senhas, tokens ou chaves em argv, variáveis de ambiente da linha de comando, arquivos temporários em texto puro ou no script de forma literal.
3. **Scripts remotos** vão pelo stdin e são embrulhados em função (`BuildScript`): `HISTFILE=/dev/null`, `DEBIAN_FRONTEND=noninteractive`, `LC_ALL=C.UTF-8`; comandos dentro do script não conseguem ler o restante do stdin.
4. **Redação de segredos.** Toda saída remota passa por `redact.LineWriter` com os segredos da conexão registrados. Ao introduzir um novo tipo de segredo (ex.: token de join, certificate-key), registre-o no `Redactor` da sessão e/ou adicione um padrão em `internal/redact`.
5. **Credenciais em repouso** só cifradas pelo `secrets.Manager` (AES-256-GCM, AAD `k8sfront:secret:<id>:<kind>`). A API nunca devolve segredos, apenas flags `has*`.
6. **Host keys** são registradas no primeiro contato (TOFU) e verificadas sempre; divergência aborta com `HostKeyMismatchError`. Reset só por ação explícita do operador (auditada).
7. **APT no Debian 13+:** sempre `DEBIAN_FRONTEND=noninteractive apt-get -y -o Dpkg::Options::="--force-confdef" -o Dpkg::Options::="--force-confold"`, repositórios em formato moderno com chave em `/etc/apt/keyrings/` (`signed-by=`). **Nunca** `apt-key`.
8. **Kubernetes:** `kubelet`, `kubeadm`, `kubectl` ficam em `apt-mark hold`; só o motor de upgrade faz `unhold` → instala versão exata → `hold` de novo. Nunca pular minor versions (version skew policy).
9. **Rolling operations:** um nó por vez, `cordon` → `drain` respeitando PDBs → ação → aguardar `Ready` → `uncordon`. Falha ou timeout pausa o job (`jobs.Pause`) antes de tocar no próximo nó (circuit breaker).
10. Toda ação administrativa relevante grava em `audit_log` (sem segredos).

## Testes

- Mudou `sshx`, `remote` ou `ops`? Rode `make test-integration`: ele usa sudo real (usuários locais `nopw`, `withpw`, `nosudo` criados pelo teste) e, se houver Docker com a imagem `debian:trixie`, executa o fluxo completo de pré-checagem num Debian 13 real.
- Testes nunca devem depender de rede externa além de Docker local.
