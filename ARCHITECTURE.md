# Arquitetura do k8sfront

Plataforma web para **provisionamento (bootstrap), gestão diária, deploy de aplicações e rolling upgrades**
de clusters Kubernetes que rodam exclusivamente sobre **Debian 13 (Trixie) ou superior**.

Status: **Fase 1 entregue** (fundação). As seções marcadas com _(Fase N)_ descrevem o desenho alvo das fases seguintes.

---

## 1. Visão geral

```
┌──────────────────────────── navegador ────────────────────────────┐
│ apps/web  React + TS + Vite · Tailwind v4 · shadcn/ui · Lucide    │
│           TanStack Query (REST) · JobStream (WebSocket) · Xterm.js│
└───────────────┬──────────────────────────────┬────────────────────┘
                │ REST /api/v1                 │ WS /api/v1/jobs/{id}/stream?after=<seq>
┌───────────────▼──────────────────────────────▼────────────────────┐
│ apps/api  (binário Go único, sem CGO)                             │
│  api ──► jobs.Engine ──► ops (handlers) ──► remote.Connector      │
│   │          │   ▲                              │                 │
│   │          ▼   │ Hub (logs em lote + fan-out) ▼                 │
│   │      store.Store (SQLite WAL │ PostgreSQL)  sshx (SSH + sudo) │
│   └──► secrets.Manager ──► vault (AES-256-GCM)       │            │
│        (Fase 3) kube (client-go) ─────────────┐      │            │
└───────────────────────────────────────────────┼──────┼────────────┘
                                                │      │ SSH (x/crypto/ssh)
                                   API Kubernetes      ▼
                                                Servidores Debian 13+
```

Princípios:

1. **O backend é dono do trabalho.** Toda operação longa é um _job_ persistido. O navegador apenas observa;
   fechar ou recarregar a aba não interrompe nada, e a reconexão retoma o stream a partir do último `seq`.
2. **Debian 13+ é pré-condição de qualquer comando.** `remote.Connector.Open` valida `/etc/os-release`
   antes de qualquer outra ação e aborta com erro claro.
3. **Segredos nunca saem do cofre em texto puro**, exceto em memória durante uma conexão, e nunca em argv,
   arquivos temporários, histórico de shell, logs ou respostas da API.
4. **Idempotência + retomada.** Scripts remotos são idempotentes; jobs podem ser reexecutados e pulam etapas
   já concluídas (`StepOnce`). Falhas pausam o pipeline (circuit breaker) em vez de seguir adiante.

---

## 2. Estrutura de diretórios

```
.
├── CLAUDE.md                 convenções, comandos e regras de segurança
├── ARCHITECTURE.md           este documento
├── Makefile                  build/test/dev
├── package.json              npm workspaces (apps/web)
├── .github/workflows/ci.yml  gofmt, vet, testes (inclui integração com sudo real + Debian 13), build web
├── apps/api/
│   ├── cmd/k8sfront-api/     main: config, cofre, banco, engine, HTTP
│   ├── internal/
│   │   ├── api/              REST + WebSocket, auth (token admin + cookie HMAC), auditoria
│   │   ├── config/           variáveis de ambiente K8SFRONT_*
│   │   ├── jobs/             Engine (máquina de estados, workers, retry/cancel/pause) e Hub (logs)
│   │   ├── ops/              handlers de job (Fase 1: servers.preflight)
│   │   ├── preflight/        coleta de fatos e avaliação de requisitos por papel
│   │   ├── redact/           redação de segredos (exata + padrões) e LineWriter
│   │   ├── remote/           Connector: SSH → valida Debian 13+ → detecta sudo
│   │   ├── secrets/          Manager: cifra/decifra via vault, persiste via store
│   │   ├── sshx/             cliente SSH, host keys, execução com sudo, os-release
│   │   ├── store/            modelo + interface Store
│   │   │   └── sqlite/       implementação SQLite (WAL) + migrations embutidas
│   │   └── vault/            AES-256-GCM
│   └── test/integration/     testes com sudo real e contêiner Debian 13 (build tag `integration`)
└── apps/web/
    └── src/
        ├── components/ui/    Button, Card, Dialog, Input, Badge, Table… (API shadcn/ui)
        ├── components/       AppShell, LogConsole (Xterm.js), tema claro/escuro, badges de status
        ├── hooks/            useJobStream
        ├── lib/              api.ts (cliente tipado), jobStream.ts (WS com retomada)
        └── pages/            Login, Servidores, Detalhe do servidor, Operações, Detalhe do job, Auditoria
```

Pacotes previstos nas próximas fases: `internal/bootstrap` (scripts kubeadm/containerd/CNI, Fase 2),
`internal/kube` (client-go, Fase 3), `internal/deploy` + `internal/helm` (Fase 4),
`internal/upgrade` + `internal/ospatch` (Fase 5).

---

## 3. Banco de dados

SQLite em **modo WAL** (`journal_mode=WAL`, `synchronous=NORMAL`, `foreign_keys=ON`, `busy_timeout=10s`,
transações `IMMEDIATE`) via driver pure-Go `modernc.org/sqlite`. Arquivo e diretório com permissão `0600/0700`.
Todo acesso passa pela interface `store.Store`; o SQL usa tipos portáveis (TEXT/INTEGER/BLOB, timestamps RFC 3339 em UTC),
então uma implementação PostgreSQL só precisa satisfazer a interface e portar as migrations.

Migrations: `internal/store/sqlite/migrations/NNNN_*.sql`, aplicadas em ordem e registradas em `schema_migrations`.

### Tabelas (0001_init.sql)

| Tabela | Finalidade | Colunas principais |
| --- | --- | --- |
| `secrets` | blobs cifrados | `id`, `kind` (ssh_private_key, ssh_key_passphrase, ssh_password, sudo_password, kubeconfig), `ciphertext`, `key_id` |
| `clusters` | clusters gerenciados (Fase 2+) | `name`, `kubernetes_version`, `pod_cidr`, `service_cidr`, `cni`, `topology`, `control_plane_endpoint`, `status`, `kubeconfig_secret_id` |
| `servers` | inventário Debian 13+ | `host`, `port`, `ssh_user`, `auth_method`, `role`, `credential_secret_id`, `passphrase_secret_id`, `sudo_secret_id`, `sudo_mode`, `os_*`, `kernel`, `last_check_*`; `UNIQUE(host, port)` |
| `known_hosts` | host keys SSH (TOFU) | `host_port` (PK), `key_type`, `public_key`, `fingerprint`, `first_seen`, `last_seen` |
| `jobs` | operações em background | `kind`, `status`, `target_type`, `target_id`, `params` (JSON), `result` (JSON), `error`, `attempts`, `created_at/started_at/finished_at` |
| `job_steps` | progresso por etapa/nó | `job_id`, `seq`, `key`, `name`, `server_id`, `status`, `error`, timestamps |
| `job_logs` | saída redigida | PK `(job_id, seq)`, `ts`, `stream` (stdout/stderr/system), `step_id`, `server_id`, `line` |
| `audit_log` | trilha de auditoria | `ts`, `actor`, `action`, `target_type`, `target_id`, `details` (JSON sem segredos), `remote_addr` |

Ao remover um servidor, seus segredos são apagados na mesma transação.

---

## 4. Cofre de credenciais (AES-256-GCM)

- Chave mestra de 32 bytes: `K8SFRONT_MASTER_KEY` (base64) ou arquivo `K8SFRONT_MASTER_KEY_FILE`
  (gerado com `crypto/rand` e permissão `0600` na primeira execução).
- Formato selado: `versão(1) | keyID(4) | nonce(12) | ciphertext+tag`. Nonce aleatório por operação.
  `keyID` = 4 primeiros bytes de SHA-256(chave), para erro claro quando a chave mestra muda.
- **AAD obrigatório** `k8sfront:secret:<id>:<kind>`: um ciphertext copiado para outra linha não decifra.
- Segredos decifrados vivem só durante a conexão e são zerados (`Wipe`) ao fechar.
- A chave de assinatura dos cookies de sessão é derivada da chave mestra (HMAC-SHA256 com rótulo próprio).

---

## 5. Motor SSH + sudo

### Conexão (`sshx.Dialer`)
- Chave privada Ed25519/RSA (com ou sem passphrase) e/ou senha (inclui keyboard-interactive); porta customizável.
- **Host key:** política TOFU por padrão (registra no primeiro contato) ou estrita (`K8SFRONT_STRICT_HOST_KEYS=true`).
  Divergência gera `HostKeyMismatchError` com as duas fingerprints. A negociação é fixada no tipo de chave registrado.
- Keepalive `keepalive@openssh.com`; timeout de conexão e handshake por contexto.

### Pipeline de abertura (`remote.Connector.Open`)
1. **connect**: decifra credenciais, conecta, verifica host key.
2. **os**: `cat /etc/os-release` (sem privilégio). Exige `ID=debian` e `VERSION_ID >= 13`; sem `VERSION_ID`
   (testing/sid) aceita apenas codinomes conhecidos ≥ trixie. Senão aborta (`ErrUnsupportedOS`).
3. **sudo**: `id -u` = 0 → `root`; `sudo -n true` OK → `nopasswd`; senão, com senha configurada (ou a senha SSH
   como fallback), `sudo -k -S -p '' -v` recebendo a senha **pelo stdin** → `password`. Caso contrário, erro claro.

### Execução (`Client.Run`)
- Linha de comando remota é **fixa e sem segredos**:
  `LC_ALL=C sudo -k -S -p '' -H -- /bin/bash --noprofile --norc -c 'echo <marcador> >&2; exec /bin/bash … -s'`
  (`sudo -n` para NOPASSWD; sem sudo para root).
- Stdin recebe: `senha\n` (apenas no modo password) → aguarda o marcador aleatório em stderr (prova de que a
  elevação ocorreu) → envia o script. `-k` ignora credenciais em cache, garantindo que a linha de senha sempre seja
  consumida pelo sudo. Se o sudo rejeitar a senha, o stdin é fechado imediatamente (sem novas tentativas com o
  conteúdo do script) e o erro é `ErrSudoAuth`.
- O script é embrulhado por `BuildScript`: preâmbulo com `HISTFILE=/dev/null`, `DEBIAN_FRONTEND=noninteractive`,
  `NEEDRESTART_MODE=a`, `LC_ALL=C.UTF-8`, `PATH` seguro, `set -o pipefail`; corpo dentro de uma função chamada com
  `</dev/null`, então nenhum comando consegue consumir o restante do script pelo stdin.
- stdout/stderr passam por `redact.LineWriter` (buffer por linha, para segredos partidos entre pacotes) com os
  segredos da conexão + padrões (blocos de chave privada, tokens `abcdef.0123456789abcdef`, `--certificate-key`,
  `Authorization: Bearer`, `token:`/`password:`/`client-key-data:`).
- Cancelamento por contexto envia `SIGKILL` à sessão; código de saída ≠ 0 vira `ExitError`.

### Padrões APT no Debian 13+ _(usados a partir da Fase 2)_
```
DEBIAN_FRONTEND=noninteractive apt-get -y -o Dpkg::Options::="--force-confdef" -o Dpkg::Options::="--force-confold" install …
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://pkgs.k8s.io/core:/stable:/v1.XX/deb/Release.key | gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg
echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v1.XX/deb/ /' > /etc/apt/sources.list.d/kubernetes.list
```
`apt-key` nunca é usado.

---

## 6. Jobs: máquina de estados e streaming

### Estados do job
```
             submit
               │
           ┌───▼────┐   worker pega   ┌─────────┐  handler OK   ┌───────────┐
           │pending ├────────────────►│ running ├──────────────►│ succeeded │
           └─▲──┬───┘                 └┬──┬──┬──┘               └───────────┘
   retry     │  │ cancel               │  │  │ erro
   ┌─────────┘  ▼                      │  │  └──────────────►┌────────┐
   │      ┌──────────┐◄────────────────┘  │                   │ failed │──┐
   │      │cancelled │   cancel           │ jobs.Pause(err)   └────────┘  │
   │      └──────────┘                    │ ou restart/shutdown           │
   │           ▲                     ┌────▼───┐                           │
   │           └────── abortar ──────┤ paused │                           │
   └─────────────────────────────────┴────────┴───────────────────────────┘
                              retry (failed | paused | cancelled → pending)
```
- Transições são atômicas no banco (`TransitionJob` com `WHERE status IN (...)`).
- Ao iniciar, jobs que estavam `running` quando o processo caiu viram `paused` ("interrupted by platform restart");
  `pending` são reenfileirados. No shutdown, jobs em execução são pausados.
- Um job por alvo (`target_type/target_id`) executa por vez; workers configuráveis (`K8SFRONT_WORKERS`).
- `attempts` incrementa a cada execução. Etapas (`job_steps`): `pending → running → succeeded | failed | skipped`.
  `StepOnce` marca como `skipped` uma etapa já concluída numa tentativa anterior (retomada idempotente).
- `jobs.Pause(err)` é o **circuit breaker**: o pipeline para no nó atual com o erro exato e o operador escolhe
  **Tentar novamente** (retry na etapa atual, pulando as concluídas) ou **Abortar fila** (cancel).

### Logs e reconexão
- Handlers emitem linhas (já redigidas) para o `Hub`. O Hub atribui `seq` estritamente crescente por job,
  grava em lotes (até 256 linhas ou 100 ms) e **só depois** faz o broadcast.
- O WebSocket inscreve o cliente **antes** de ler o backlog e deduplica por `seq`, portanto nenhuma linha se perde
  nem se repete entre o replay e o tempo real.
- Assinante lento é desconectado com `1013 Try Again Later`; o cliente reconecta com `after=<último seq>`.
- O cliente (`JobStream`) reconecta com backoff exponencial com jitter (até 15 s) preservando o cursor.

---

## 7. API

Base `/api/v1`. JSON em todas as requisições com corpo (`Content-Type: application/json` obrigatório em métodos
de escrita; defesa extra contra CSRF). Erros: `{"error": "<código>", "message": "..."}`.

### Autenticação
Admin único: `K8SFRONT_ADMIN_TOKEN` (≥ 16 caracteres) ou token gerado em `data/admin.token`.
`POST /auth/login` emite cookie `k8sfront_session` (HttpOnly, SameSite=Strict, `Secure` com
`K8SFRONT_SECURE_COOKIES=true`, assinado com HMAC, expira em `K8SFRONT_SESSION_TTL`).
Automação pode usar `Authorization: Bearer <token>`. (RBAC multiusuário: fora do escopo atual.)

### Endpoints (Fase 1)

| Método | Rota | Descrição |
| --- | --- | --- |
| GET | `/health` | liveness (público) |
| POST | `/auth/login` · `/auth/logout` | sessão |
| GET | `/auth/me` | sessão atual |
| GET · POST | `/servers` | listar · cadastrar (segredos cifrados, só flags `has*` na resposta) |
| GET · PATCH · DELETE | `/servers/{id}` | detalhar · editar (segredos opcionais, `clearSudoPassword`) · remover |
| GET · DELETE | `/servers/{id}/host-key` | ver fingerprint · esquecer (após reinstalação) |
| POST | `/servers/{id}/preflight` | **Testar Conexão & Pré-requisitos** → `202` + job |
| POST | `/preflight` | `{"serverIds": [...]}` testa vários nós em paralelo num único job |
| GET | `/jobs?status=&targetId=&limit=` | listar jobs |
| GET | `/jobs/{id}` | job + etapas + `lastLogSeq` |
| GET | `/jobs/{id}/logs?after=&limit=` | logs paginados por `seq` |
| POST | `/jobs/{id}/cancel` · `/jobs/{id}/retry` | abortar · reexecutar |
| WS | `/jobs/{id}/stream?after=<seq>` | stream: `{"type":"job"}` → backlog `{"type":"log"}` → `{"type":"ready"}` → tempo real |
| GET | `/audit` | trilha de auditoria |

### Pré-checagem (`servers.preflight`)
Por servidor, em paralelo: conexão + host key → Debian 13+ → sudo → coleta de fatos sem privilégio
(arquitetura, vCPU, RAM, disco livre em `/var/lib`, swap, NTP + desvio de relógio vs. plataforma, portas TCP em
escuta via `/proc/net/tcp*`, cgroup v2 + systemd). Requisitos por papel:

| | Control Plane | Worker |
| --- | --- | --- |
| vCPU | ≥ 2 | ≥ 1 |
| RAM | ≥ 1700 MiB | ≥ 1 GiB |
| Disco `/var/lib` | ≥ 10 GiB (aviso < 20 GiB) | idem |
| Portas livres | 6443, 2379-2380, 10250, 10257, 10259 | 10250, 10256 (aviso se houver algo em 30000-32767) |
| Relógio | falha > 30 s, aviso > 2 s ou NTP não sincronizado | idem |

O relatório (`pass | warn | fail` por item) fica no resultado do job e em `servers.last_check_report`.

### Endpoints previstos
- _(Fase 2)_ `GET /kubernetes/versions` (versões estáveis de `pkgs.k8s.io`), `POST /clusters` (wizard),
  `POST /clusters/{id}/bootstrap`, `POST /clusters/{id}/nodes` (scale-up), `DELETE /clusters/{id}/nodes/{name}`.
- _(Fase 3)_ `/clusters/{id}/k8s/...` (overview, nodes + cordon/uncordon/drain, workloads, rede, storage, configmaps,
  secrets mascarados, eventos), WS de logs de pod (`follow`) e WS de `exec` (terminal interativo).
- _(Fase 4)_ `POST /clusters/{id}/apply` (`dryRun=server`), `/clusters/{id}/helm/releases|repos|charts`.
- _(Fase 5)_ `/clusters/{id}/upgrades/plan|start`, `/servers/{id}/os-updates`, `/clusters/{id}/rolling-reboot`.

---

## 8. Desenho das próximas fases

### Fase 2 · Bootstrap (`cluster.bootstrap`)
Etapas por nó (todas `StepOnce`, scripts idempotentes): `preflight` → `swap` (swapoff, `/etc/fstab`, mask de
unidades `.swap`) → `kernel` (`/etc/modules-load.d/k8s.conf`: overlay, br_netfilter; `/etc/sysctl.d/k8s.conf`) →
`containerd` (`SystemdCgroup = true`) → `k8s-repo` (keyring em `/etc/apt/keyrings`) → `k8s-packages`
(versão exata + `apt-mark hold`) → `kubeadm init` no primeiro control plane (kube-vip opcional) → CNI (Cilium
ou Calico) → metrics-server opcional → join dos control planes adicionais (`--certificate-key`) → join dos
workers → remoção opcional do taint (single-node) → extração de `admin.conf` para o cofre → aguarda todos `Ready`.
Tokens de join e certificate-key são registrados no `Redactor` da sessão.

### Fase 3 · Gestão via client-go
Kubeconfig decifrado em memória por cluster; informers para nós/eventos; drain com Eviction API (respeita PDB,
ignora DaemonSets, trata `emptyDir`). Logs e exec via WebSocket (Xterm.js interativo).

### Fase 4 · Central de deploy
Formulário visual → manifests gerados no backend (Deployment/Service/Ingress/PVC/imagePullSecret) com preview YAML;
editor Monaco com `dry-run=server` antes do apply (server-side apply); Helm via SDK Go.

### Fase 5 · Updates & Lifecycle
- **Upgrade Kubernetes (`cluster.upgrade`)**: plano calcula a cadeia de minors (`1.31 → 1.32 → 1.33`, nunca pula).
  Pré-checks: `kubeadm upgrade plan`, todos os nós `Ready`, saúde do etcd, APIs depreciadas, PDBs bloqueantes,
  **snapshot do etcd**. Ordem: CP primário (`kubeadm upgrade apply`) → CPs adicionais (`kubeadm upgrade node`) →
  workers **um por vez** (cordon → drain → kubeadm → kubelet/kubectl → restart → `Ready` → uncordon).
  Cada nó é uma etapa `StepOnce`; erro ou timeout → `jobs.Pause` (circuit breaker) antes do próximo nó.
- **OS patching (`servers.os-update`)**: `apt-get update`, lista de atualizáveis com destaque para
  `trixie-security`, aplicação nó a nó preservando `apt-mark hold`, detecção de `/var/run/reboot-required`.
- **Rolling reboot**: cordon + drain → `systemctl reboot` → aguarda SSH voltar → aguarda `Ready` → uncordon → próximo.
- `docker-compose.yml` com a API servindo a SPA e volume para `/data`.

---

## 9. Configuração

| Variável | Padrão | Descrição |
| --- | --- | --- |
| `K8SFRONT_ADDR` | `:8080` | endereço HTTP |
| `K8SFRONT_DATA_DIR` | `./data` | banco, chave mestra, token admin |
| `K8SFRONT_DB_PATH` | `<data>/k8sfront.db` | arquivo SQLite |
| `K8SFRONT_MASTER_KEY` / `_FILE` | `<data>/master.key` | chave AES-256 (base64) |
| `K8SFRONT_ADMIN_TOKEN` | gerado em `<data>/admin.token` | token de login |
| `K8SFRONT_WEB_DIR` | vazio | diretório da SPA compilada |
| `K8SFRONT_ALLOWED_ORIGINS` | vazio | origens extras para WebSocket (ex.: `localhost:5173`) |
| `K8SFRONT_WORKERS` | `4` | workers de job |
| `K8SFRONT_STRICT_HOST_KEYS` | `false` | rejeitar hosts desconhecidos |
| `K8SFRONT_SESSION_TTL` | `12h` | duração da sessão |
| `K8SFRONT_SECURE_COOKIES` | `false` | cookie `Secure` (atrás de HTTPS) |
| `K8SFRONT_LOG_LEVEL` | `info` | debug, info, warn, error |
