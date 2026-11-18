# Arquitetura do k8sfront

Plataforma web para **provisionamento (bootstrap), gestão diária, deploy de aplicações e rolling upgrades**
de clusters Kubernetes que rodam exclusivamente sobre **Debian 13 (Trixie) ou superior**.

Status: **Fases 1 e 2 entregues** (fundação + bootstrap com Wizard de Deploy); **Fase 3** (Visão Geral, Topologia ao vivo, Workloads e Rede & Storage em modo leitura); **Fase 4** (Central de Deploy: formulário → manifests, editor Monaco com dry-run=server e server-side apply, Helm); **Fase 5** (Updates & Lifecycle: upgrade de versão, nós, patches do SO, rolling reboot, certificados). Itens ainda marcados com _(Fase N)_ são desenho alvo.

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

Fase 2 adicionou `internal/bootstrap` (spec, configs kubeadm v1beta4, scripts embutidos em `scripts/*.sh`),
`internal/k8sver` (versões estáveis de `dl.k8s.io`) e `internal/ops/bootstrap.go`.
Fase 3 adicionou `internal/kube` (client-go). Fase 4 adicionou `internal/deploy` (formulário → manifests, parse de YAML, server-side apply, releases Helm por metadados).
Fase 5 adicionou `internal/upgrade` (plano de versões e pré-checagens), `internal/ospatch` (parse do estado do APT) e os jobs de lifecycle em `internal/ops`
(scripts em `internal/bootstrap/scripts/`, renderizados por `internal/bootstrap/lifecycle.go`).
Registries & Imagens adicionou `internal/registry` (presets, validação, parse de referências de imagem, `hosts.toml`,
dockerconfigjson e teste de credenciais), `internal/kube/registries.go` (imagePullSecrets, catálogo e pré-pull) e
`internal/ops/registries.go` (jobs `cluster.registries-sync`, `cluster.image-prepull`, `cluster.image-prune`).

---

## 3. Banco de dados

SQLite em **modo WAL** (`journal_mode=WAL`, `synchronous=NORMAL`, `foreign_keys=ON`, `busy_timeout=10s`,
transações `IMMEDIATE`) via driver pure-Go `modernc.org/sqlite`. Arquivo e diretório com permissão `0600/0700`.
Todo acesso passa pela interface `store.Store`; o SQL usa tipos portáveis (TEXT/INTEGER/BLOB, timestamps RFC 3339 em UTC),
então uma implementação PostgreSQL só precisa satisfazer a interface e portar as migrations.

Migrations: `internal/store/sqlite/migrations/NNNN_*.sql`, aplicadas em ordem e registradas em `schema_migrations`.

### Tabelas (0001_init.sql + 0002_cluster_options.sql + 0003_registries.sql + 0004_users.sql)

| Tabela | Finalidade | Colunas principais |
| --- | --- | --- |
| `secrets` | blobs cifrados | `id`, `kind` (ssh_private_key, ssh_key_passphrase, ssh_password, sudo_password, kubeconfig), `ciphertext`, `key_id` |
| `clusters` | clusters gerenciados (Fase 2+) | `name`, `kubernetes_version`, `pod_cidr`, `service_cidr`, `cni`, `topology`, `control_plane_endpoint`, `status` (draft/provisioning/ready/failed), `kubeconfig_secret_id`, `options` (JSON: `endpointMode` local-proxy/kube-vip/direct, kube-vip, untaint, metrics-server, versão do CNI), `api_server_url` |
| `servers` | inventário Debian 13+ | `host`, `port`, `ssh_user`, `auth_method`, `role`, `credential_secret_id`, `passphrase_secret_id`, `sudo_secret_id`, `sudo_mode`, `os_*`, `kernel`, `last_check_*`; `UNIQUE(host, port)` |
| `known_hosts` | host keys SSH (TOFU) | `host_port` (PK), `key_type`, `public_key`, `fingerprint`, `first_seen`, `last_seen` |
| `jobs` | operações em background | `kind`, `status`, `target_type`, `target_id`, `params` (JSON), `result` (JSON), `error`, `attempts`, `created_at/started_at/finished_at` |
| `job_steps` | progresso por etapa/nó | `job_id`, `seq`, `key`, `name`, `server_id`, `status`, `error`, timestamps |
| `job_logs` | saída redigida | PK `(job_id, seq)`, `ts`, `stream` (stdout/stderr/system), `step_id`, `server_id`, `line` |
| `audit_log` | trilha de auditoria | `ts`, `actor`, `action`, `target_type`, `target_id`, `details` (JSON sem segredos), `remote_addr` |
| `registries` | registries de imagens e mirrors | `name`, `kind` (dockerhub/ghcr/quay/harbor/generic), `host` (UNIQUE, como aparece nas imagens), `endpoint`, `mirrors` (JSON), `username`, `password_secret_id` (`registry_password` no cofre), `ca_cert` (PEM público), `skip_verify`, `pull_secret`, `namespaces` (JSON, vazio = todos menos os de sistema), `cluster_ids` (JSON, vazio = todos) |

| `users` | contas do k8sfront | `username` (UNIQUE, sem diferenciar maiúsculas), `display_name`, `email`, `source` (local/ldap), `role` (admin/operator/viewer), `password_hash` (argon2id, só locais), `disabled`, `external_dn`, `groups` (JSON, último login LDAP), `session_epoch`, `last_login_at` |
| `settings` | documentos JSON pequenos | `key` (ex.: `ldap`), `value`, `updated_at` |

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

### Padrões APT no Debian 13+
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

### Autenticação e papéis
- **Usuários locais** (`users`, senha em hash argon2id PHC, mínimo 10 caracteres) e **LDAP / Active Directory**
  (`internal/ldap`: cliente LDAPv3 próprio sobre a stdlib com bind simples, busca, LDAPS e StartTLS). O login tenta a conta
  local primeiro; uma conta local sempre vence uma do diretório com o mesmo nome.
- **LDAP/AD:** bind com a conta de serviço → busca o usuário pelo filtro (`{username}` com escape RFC 4515) → lê os grupos
  (`memberOf` + busca de grupos com `{dn}`; no AD a regra `1.2.840.113556.1.4.1941` resolve grupos aninhados) → bind como o
  usuário (senha vazia é recusada antes de ir ao servidor). O papel é o maior entre os grupos mapeados (nome/CN ou DN) e é
  recalculado a cada login; sem grupo mapeado e sem papel padrão, o login é negado. A configuração fica em `settings`
  (`ldap`) e a senha da conta de serviço no cofre (`ldap_bind_password`).
- **Papéis:** `viewer` (leitura), `operator` (tudo menos usuários e autenticação) e `admin`. Em `api.Handler`, `priv`
  exige `viewer` para GET e `operator` para o resto; `opr`/`adm` sobem o mínimo (kubeconfig e auditoria = operator;
  `/users` e `/auth/ldap*` = admin). O papel é lido do banco a cada requisição.
- **Último admin:** `UpdateUser`/`DeleteUser` rodam numa transação que é desfeita (`ErrLastAdmin`, 409) se a mudança
  levar de ≥ 1 para 0 administradores locais ativos. Ninguém muda o próprio papel, se desativa ou se exclui.
- **Sessão:** cookie `k8sfront_session` (HttpOnly, SameSite=Strict, `Secure` com `K8SFRONT_SECURE_COOKIES=true`, HMAC,
  expira em `K8SFRONT_SESSION_TTL`) com `exp | nonce | session_epoch | user id`. Desativar o usuário ou trocar a senha
  incrementa `session_epoch` e derruba as sessões abertas.
- **Token de administrador** (`K8SFRONT_ADMIN_TOKEN` ≥ 16 caracteres ou `data/admin.token`): acesso de emergência como
  admin, no login ou em `Authorization: Bearer <token>` para automação. Aparece na auditoria como `admin (token)`.
- Auditoria: `auth.login`, `auth.login_failed` (com o motivo), `auth.password_change`, `user.create|update|delete`,
  `auth.ldap_update`, `auth.ldap_test`; `actor` é o usuário da sessão e jobs gravam `createdBy` com ele.

| Método | Rota | Papel | Descrição |
| --- | --- | --- | --- |
| GET | `/auth/providers` | público | `{users, ldap, ldapKind}` para a tela de login |
| POST | `/auth/login` | público | `{username, password}` ou `{token}` |
| GET | `/auth/me` | viewer | usuário, papel e origem da sessão |
| POST | `/auth/password` | viewer | troca da própria senha (contas locais) |
| GET · POST | `/users` | admin | listar · criar usuário local (resposta só com `hasPassword`) |
| PATCH · DELETE | `/users/{id}` | admin | nome, e-mail, papel, senha, desativar · excluir |
| GET · PUT | `/auth/ldap` | admin | configuração (só `hasBindPassword`, nunca a senha) |
| POST | `/auth/ldap/test` | admin | testa o formulário sem salvar; com usuário/senha mostra grupos e papel |

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

### Endpoints (Fase 2)

| Método | Rota | Descrição |
| --- | --- | --- |
| GET | `/kubernetes/versions` | últimas 4 minors estáveis (`dl.k8s.io/release/stable-1.N.txt`, cache de 1 h) |
| GET · POST | `/clusters` | listar · criar pelo wizard (`start: true` já dispara o bootstrap) |
| GET · DELETE | `/clusters/{id}` | detalhe (nós, `hasKubeconfig`, job de bootstrap) · remover (desvincula servidores, apaga kubeconfig) |
| POST | `/clusters/{id}/bootstrap` | inicia ou **retoma** o bootstrap (reexecuta o job falho/pausado; 409 se já estiver rodando) |
| GET | `/clusters/{id}/kubeconfig` | download do `admin.conf` decifrado (auditado) |

### Endpoints (Fase 3, leitura)

`internal/kube` decifra o kubeconfig do cofre só para montar o `rest.Config` (os bytes são zerados em seguida) e
mantém um client-go por cluster, refeito quando o segredo muda. As respostas usam visões enxutas (`views.go`):
objetos crus nunca vão ao navegador (env de pods pode ter valores literais) e Secrets nunca são lidos.

| Método | Rota | Descrição |
| --- | --- | --- |
| GET | `/clusters/{id}/k8s/overview` | versão, nós com requests/uso (metrics-server se houver), pods por fase, control plane, add-ons, eventos Warning |
| GET | `/clusters/{id}/k8s/namespaces` | nomes |
| GET | `/clusters/{id}/k8s/workloads?namespace=` | Deployments, StatefulSets, DaemonSets e pods |
| GET | `/clusters/{id}/k8s/network?namespace=` | Services, Ingresses, PVCs, PVs, StorageClasses, contagem de NetworkPolicies |
| WS | `/clusters/{id}/k8s/topology/stream` | `status` → `snapshot` (nós, pods, services) → `upsert`/`delete` em tempo real |

Erros: 409 `not_ready` sem kubeconfig, 502 `upstream` se a API do cluster não responde, 403 se recusar a credencial.
A topologia usa informers compartilhados que só rodam enquanto há alguém assistindo (+2 min de folga); assinante
lento é desconectado com `1013` e o cliente reconecta recebendo um snapshot novo. Um `/readyz` a cada 15 s alterna
o estado entre `live` e `error`.
A página tem modo tela cheia para telões de NOC (botão, tecla `F` ou `?kiosk=1` na URL, que abre já em tela cheia
sem precisar de clique) com relógio, contadores grandes e a tela mantida acesa via Wake Lock quando o navegador permite.

### Endpoints (Fase 4, Central de Deploy)

| Método | Rota | Descrição |
| --- | --- | --- |
| POST | `/deploy/render` | formulário (`AppSpec`) → YAML (Namespace, PVC, Deployment, Service, Ingress); credenciais de registry nunca entram no preview |
| POST | `/clusters/{id}/deploy/app` | `{app, dryRun}`: renderiza, cria o Secret dockerconfigjson (só aqui) e aplica |
| POST | `/clusters/{id}/apply` | `{manifest, namespace, dryRun}`: YAML multi-documento (até 200 objetos), Namespaces/CRDs primeiro, server-side apply (`fieldManager=k8sfront`, `force`), `dryRun=All` |
| POST | `/clusters/{id}/delete` | `{manifest \| objects[] \| app, namespace, keepNamespaces, dryRun}`: exclui em ordem inversa (Namespaces por último, propagação `Background`); resultado `deleted`/`absent`/`error`; nada em `kube-system`, `kube-public`, `kube-node-lease` (nem o namespace `default` nem o Service `default/kubernetes`). A UI sempre roda o dry-run antes e pede o nome digitado; auditado como `deploy.delete` |
| GET | `/clusters/{id}/k8s/workloads/{ns}/{kind}/{name}/related` | Services cujo seletor pega só este workload (nenhum outro) e Ingresses cujos backends são todos esses Services; a UI oferece excluí-los junto |
| GET | `/clusters/{id}/k8s/namespaces-usage[/{ns}]` | Inventário por namespace (só metadados de Secrets/ConfigMaps): workloads, pods, Services, Ingresses, PVCs, releases Helm e outros objetos criados por pessoas; base da aba Namespaces e dos avisos "namespace ainda em uso" / "excluir também o namespace" no diálogo de exclusão (`alsoNamespaces` em `/delete`) |
| GET | `/servers/{id}/removal` | Prévia de "Remover servidor": fora de cluster só sai do inventário; worker de cluster lista os pods do nó (movidos, DaemonSet, bloqueiam o drain) e avisos; control plane ou cluster sem bootstrap vem com `blocked` |
| POST | `/servers/{id}/remove` | Servidor livre: apaga do inventário (200). Worker: job `cluster.node-remove` com `deleteServer` (cordon → drain respeitando PDBs → delete node → kubeadm reset → apaga do inventário; falha no drain pausa o job). Control plane: 409 |
| GET | `/clusters/{id}/helm/releases` | releases pelos **metadados** dos Secrets `owner=helm` (o payload com values nunca é lido) |
| POST | `/clusters/{id}/helm` | job `cluster.helm`: `helm upgrade --install --wait` ou `uninstall` no CP primário; values cifrados no cofre (`helm_values`) e apagados no sucesso |

O apply é síncrono (uma chamada por objeto, timeout de 60 s) e continua após erros, como o kubectl. A UI só libera
“Aplicar” depois de um dry-run sem erros do mesmo conteúdo. Helm roda via CLI no CP primário (binário `v4.x` com
sha256 verificado), e não via SDK Go, para não trazer a árvore de dependências do Helm para o binário.

### Endpoints (Fase 5, Updates & Lifecycle)

| Método | Rota | Descrição |
| --- | --- | --- |
| GET | `/clusters/{id}/upgrades/plan` | versão da API, nós, pré-checagens (Ready, skew, PDBs, upgrade pendente) e destinos com a cadeia de minors |
| POST | `/clusters/{id}/upgrades` | `{targetVersion}` (precisa estar no plano) → job `cluster.upgrade` |
| POST · DELETE | `/clusters/{id}/nodes` · `/clusters/{id}/nodes/{serverId}` | adicionar (`{serverId, role}`) / remover worker ou control plane (`cluster.node-add` / `cluster.node-remove`) |
| POST | `/clusters/{id}/k8s/nodes/{name}/cordon` · `uncordon` · `drain` | cordon/uncordon imediatos (client-go); drain é job (`cluster.node-drain`) |
| POST | `/clusters/{id}/os-updates/check` · `/clusters/{id}/os-updates` | `cluster.os-check` (paralelo) · `cluster.os-update` `{serverIds, reboot: never\|if-required\|always}` |
| POST | `/clusters/{id}/rolling-reboot` | `cluster.rolling-reboot` `{serverIds}` |
| POST | `/clusters/{id}/certs` | `{action: check\|renew}` → `cluster.certs` |

Jobs de manutenção que alteram o cluster (upgrade, nós, SO, reboot, renovação) são exclusivos: 409 se outro estiver pendente/rodando.

### Endpoints (Registries & Imagens)

| Método | Rota | Descrição |
| --- | --- | --- |
| GET · POST | `/registries` | listar · cadastrar (Docker Hub, GHCR e Quay têm host/endpoint fixos; Harbor e genérico pedem o host). A senha/token vai para o cofre; a resposta só traz `hasPassword` |
| GET · PATCH · DELETE | `/registries/{id}` | detalhar · editar (`password` troca e apaga o segredo antigo, `clearPassword`) · remover (apaga o segredo junto) |
| POST | `/registries/{id}/check` | `GET /v2/` no endpoint; responde a desafios Bearer (servidor de token) ou Basic com as credenciais guardadas e devolve só ok/mensagem |
| POST | `/clusters/{id}/registries/sync` | job `cluster.registries-sync` (exclusivo): nó a nó, grava `/etc/containerd/certs.d/<host>/hosts.toml` (+ `ca.crt`), remove diretórios que o k8sfront criou para registries que não existem mais e liga `config_path` no containerd 1.7 (reinicia o containerd só nessa primeira vez; containers seguem rodando). Falha pausa antes do próximo nó. Depois reconcilia os imagePullSecrets |
| GET | `/clusters/{id}/k8s/images` | catálogo: pods (o que roda, por workload) + `node.status.images` (onde está em cache e tamanho; o kubelet lista só as 50 maiores por nó) |
| POST | `/clusters/{id}/images/prepull` | `{images}` (até 20) → job `cluster.image-prepull` |
| POST | `/clusters/{id}/images/prune` | `{serverIds?}` → job `cluster.image-prune` (`crictl rmi --prune` em paralelo) |

- **imagePullSecrets:** para cada registry com usuário, senha e `pull_secret`, o Secret `k8sfront-registry-<nome>`
  (`kubernetes.io/dockerconfigjson`, labels `app.kubernetes.io/managed-by=k8sfront` e `k8sfront.io/registry=<id>`) é
  criado em cada namespace alvo e adicionado à ServiceAccount `default`. Secrets com esses labels que não são mais
  desejados saem (listados só por metadados). Credenciais nunca vão para os nós: nada de `crictl --creds` em argv.
- **Central de Deploy:** o formulário aceita `pullSecret.registryId` e, sem pull secret, escolhe sozinho o registry
  cujo host bate com o da imagem. O Secret compartilhado nunca é excluído junto com o app. Depois de um apply
  (formulário ou YAML) os namespaces tocados recebem os pull secrets; no YAML, namespaces que já existem recebem antes.
- **Pré-pull:** DaemonSet temporário em `k8sfront-system` (tolera todos os taints, requests mínimos), um container por
  imagem com comando inexistente (`/k8sfront-prepull-noop`): o kubelet baixa a imagem com os imagePullSecrets e o
  entrypoint da imagem nunca roda. O job acompanha o estado de cada container por nó (pull ok, erro de pull ou pendente,
  timeout de 15 min) e remove o DaemonSet no fim.
- Clusters novos já saem do bootstrap com `config_path = '/etc/containerd/certs.d'` (script `containerd`).

### Endpoints previstos
- _(Fase 3)_ configmaps, secrets mascarados, WS de logs de pod (`follow`) e WS de `exec` (terminal interativo).

---

## 8. Bootstrap (Fase 2, `cluster.bootstrap`)

`bootstrap.Spec.Validate` recusa: nome fora de DNS label, minor < 1.31, CIDRs sobrepostos/pequenos, IPs de nós ou VIP
dentro dos CIDRs, mais de um CP no modo `direct`, kube-vip junto com o proxy local, servidores duplicados;
`ValidateNew` (só na criação) recusa também número par de control planes. Pipeline (`internal/ops/bootstrap.go`):

1. **Preparação em paralelo** por nó (`connect` refaz a validação Debian 13+ a cada execução; demais etapas `StepOnce`):
   `swap` (swapoff, `/etc/fstab`, `systemctl mask` das unidades `.swap`) → `kernel` (overlay, br_netfilter,
   sysctl) → `containerd` (`SystemdCgroup = true`, pause image alinhada ao kubeadm) → `apiproxy` (só no modo
   proxy local, ver abaixo) → `k8s-repo` (keyring em
   `/etc/apt/keyrings`, `signed-by=`) → `k8s-packages` (versão exata + `apt-mark hold`) → `images` (só CPs).
2. `init` no primeiro control plane (kube-vip em modo ARP como static pod, usando `super-admin.conf` no init e
   `admin.conf` depois em `kube-vip-finalize`).
3. `kubeconfig`: `admin.conf` e `ca.crt` lidos em modo `Quiet` (nunca vão ao log), kubeconfig cifrado no cofre.
4. `cni`: Cilium via cilium-cli (binário com sha256 verificado) ou Calico via tigera-operator.
5. `join-credentials` (sempre roda, recria token com TTL curto e reenvia certs para HA) → joins dos CPs **em série**
   → joins dos workers em paralelo. Hash da CA calculado no Go.
6. Opcionais: `untaint` (single-node ou opção marcada), `metrics-server`.
7. `ready`: aguarda todos os nós `Ready` (`ReadyTimeout`), então o cluster vai para `ready`; erro → `failed`.

Segredos (bootstrap token, certificate-key) são gerados no Go, registrados no `Redactor` de cada sessão e entregues
ao kubeadm por process substitution (`--config <(cat <<'EOF' ...)`) dentro do script enviado via stdin: nunca em
argv, nunca em disco. Versões fixadas em `internal/bootstrap/spec.go` (Cilium 1.20.4 / cilium-cli v0.20.2,
Calico v3.32.3, metrics-server v0.8.1, kube-vip v1.3.1). Retomar um bootstrap pula as etapas `StepOnce` já concluídas.

### Endpoint da API e alta disponibilidade

`Options.EndpointMode` define o `controlPlaneEndpoint`:

| Modo | Endpoint | Como funciona |
| --- | --- | --- |
| `local-proxy` (padrão de clusters novos) | `k8s-api.k8sfront.internal:7445` | Cada nó (CPs e workers) roda o HAProxy do Debian como `k8sfront-apiproxy.service` em `127.0.0.1:7445`, com health check HTTPS `/readyz` em todos os kube-apiservers (`:6443`) e `on-marked-down shutdown-sessions`. O nome aponta para 127.0.0.1 em `/etc/hosts` e é SAN do certificado da API. É serviço do host, não static pod, porque o `kubeadm join` precisa do endpoint antes de existir kubelet no nó. |
| `kube-vip` | `VIP:6443` | VIP flutuante em ARP (static pod nos CPs). |
| `direct` | `IP do 1º CP:6443` | Clusters antigos (options sem `endpointMode` e sem VIP); só 1 CP. |

O k8sfront e o kubectl externo não têm proxy local: no modo `local-proxy` o `kube.Manager` disca os CPs com
failover (`failoverDialer`, verificando TLS pelo nome do endpoint) e o download do kubeconfig troca o `server` pelo
primeiro CP (com `tls-server-name`) e lista os demais no cabeçalho.

Adicionar control plane (`cluster.node-add` com `role=control-plane`): preparação do nó (inclui o proxy com a lista
nova) → `apiproxy-add` em todos os outros nós → `join-credentials` (com upload-certs) → `kubeadm join --control-plane`
→ `Ready`. Remover (`cluster.node-remove`): drain → `apiproxy-remove` nos outros nós → `kubeadm reset` no nó (sai do
etcd e remove o proxy) → `etcd-member` (confere/remove o membro via etcdctl em outro CP) → `kubectl delete node`.
O primário do job nunca é o nó alvo. Contagens pares geram aviso (o etcd tolera as mesmas falhas que com um a menos);
o único CP não pode ser removido.

As configs geradas são validadas contra o kubeadm real quando `K8SFRONT_KUBEADM=/caminho/kubeadm` está definido
em `go test ./internal/bootstrap/`.

## 8b. Desenho das próximas fases

### Fase 3 · Gestão via client-go
Kubeconfig decifrado em memória por cluster; informers para nós/eventos; drain com Eviction API (respeita PDB,
ignora DaemonSets, trata `emptyDir`). Logs e exec via WebSocket (Xterm.js interativo).

### Fase 4 · Central de deploy (entregue)
Formulário visual → manifests gerados no backend (Deployment/Service/Ingress/PVC/imagePullSecret) com preview YAML;
editor Monaco (empacotado localmente, sem CDN) com `dry-run=server` antes do apply (server-side apply); Helm via CLI no CP primário.

### Fase 5 · Updates & Lifecycle (entregue)
Implementado como desenhado abaixo. Detalhes de execução: o nome do nó no Kubernetes é resolvido pelo InternalIP (ou
hostname); o drain roda `kubectl drain` no CP primário (Eviction API) com timeout de 10 min; em cluster de nó único só
há cordon. O reboot é agendado com `systemd-run` e o job espera o SSH voltar com **novo boot id** (refazendo a
validação Debian 13+) e o kubelet ativo. A renovação de certificados recoloca os static pods um a um e regrava o
`admin.conf` no cofre. Snapshots do etcd ficam em `/var/backups/k8sfront` (últimos 5) no CP primário.
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
