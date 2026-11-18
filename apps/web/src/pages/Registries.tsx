import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, CircleAlert, KeyRound, Loader2, Pencil, PlugZap, Plus, Trash2, UploadCloud } from "lucide-react";
import { api, isFinal, type Cluster, type Job, type Registry, type RegistryCheck, type RegistryInput, type RegistryKind } from "@/lib/api";
import { formatDate } from "@/lib/utils";
import { PageHeader } from "@/components/layout/AppShell";
import { JobStatusBadge } from "@/components/status";
import { ConfirmButton, ErrorText, Field } from "@/components/deploy/shared";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input, NativeSelect, Textarea } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

export const kindLabel: Record<RegistryKind, string> = {
  dockerhub: "Docker Hub",
  ghcr: "GitHub (GHCR)",
  quay: "Quay.io",
  harbor: "Harbor",
  generic: "Privado / mirror",
};

const presetHost: Partial<Record<RegistryKind, string>> = { dockerhub: "docker.io", ghcr: "ghcr.io", quay: "quay.io" };

const splitList = (s: string) =>
  s
    .split(/[\s,]+/)
    .map((x) => x.trim())
    .filter(Boolean);

interface FormState {
  kind: RegistryKind;
  name: string;
  host: string;
  endpoint: string;
  mirrors: string;
  username: string;
  password: string;
  clearPassword: boolean;
  caCert: string;
  skipVerify: boolean;
  pullSecret: boolean;
  namespaces: string;
  clusterIds: string[];
}

function toForm(r?: Registry): FormState {
  return {
    kind: r?.kind ?? "dockerhub",
    name: r?.name ?? "dockerhub",
    host: r?.host ?? "",
    endpoint: r?.endpoint ?? "",
    mirrors: (r?.mirrors ?? []).join("\n"),
    username: r?.username ?? "",
    password: "",
    clearPassword: false,
    caCert: r?.caCert ?? "",
    skipVerify: r?.skipVerify ?? false,
    pullSecret: r?.pullSecret ?? true,
    namespaces: (r?.namespaces ?? []).join(", "),
    clusterIds: r?.clusterIds ?? [],
  };
}

function toInput(f: FormState, editing: boolean): RegistryInput {
  const fixed = !!presetHost[f.kind];
  const input: RegistryInput = {
    kind: f.kind,
    name: f.name.trim(),
    host: fixed ? "" : f.host.trim(),
    endpoint: fixed ? "" : f.endpoint.trim(),
    mirrors: splitList(f.mirrors),
    username: f.username.trim(),
    caCert: f.caCert,
    skipVerify: f.skipVerify,
    pullSecret: f.pullSecret,
    namespaces: splitList(f.namespaces),
    clusterIds: f.clusterIds,
  };
  if (f.password) input.password = f.password;
  if (editing && f.clearPassword) input.clearPassword = true;
  return input;
}

function Check({ checked, onChange, children }: { checked: boolean; onChange: (v: boolean) => void; children: React.ReactNode }) {
  return (
    <label className="flex cursor-pointer items-start gap-2 text-sm">
      <input type="checkbox" className="accent-primary mt-0.5 size-4" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      <span>{children}</span>
    </label>
  );
}

function RegistryDialog({ registry, clusters, onClose }: { registry?: Registry; clusters: Cluster[]; onClose: () => void }) {
  const qc = useQueryClient();
  const [f, setF] = useState<FormState>(() => toForm(registry));
  const set = (p: Partial<FormState>) => setF((x) => ({ ...x, ...p }));
  const fixed = presetHost[f.kind];
  const save = useMutation({
    mutationFn: () => (registry ? api.updateRegistry(registry.id, toInput(f, true)) : api.createRegistry(toInput(f, false))),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["registries"] });
      onClose();
    },
  });
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{registry ? `Editar ${registry.name}` : "Novo registry"}</DialogTitle>
          <DialogDescription>
            A senha ou token fica cifrada no cofre (AES-256-GCM) e só sai de lá para criar os imagePullSecrets no cluster. Ela nunca volta para a tela.
          </DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            save.mutate();
          }}
        >
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Tipo">
              <NativeSelect
                value={f.kind}
                disabled={!!registry}
                onChange={(e) => {
                  const kind = e.target.value as RegistryKind;
                  set({ kind, name: !registry && (f.name === f.kind || !f.name) ? kind : f.name });
                }}
              >
                {(Object.keys(kindLabel) as RegistryKind[]).map((k) => (
                  <option key={k} value={k}>
                    {kindLabel[k]}
                  </option>
                ))}
              </NativeSelect>
            </Field>
            <Field label="Nome" hint="Vira o nome do Secret: k8sfront-registry-<nome>.">
              <Input value={f.name} onChange={(e) => set({ name: e.target.value.toLowerCase() })} placeholder="dockerhub" required />
            </Field>
            {fixed ? (
              <Field label="Host" className="sm:col-span-2" hint="Fixo para este tipo. Imagens sem host (nginx, bitnami/redis) são do Docker Hub.">
                <Input value={fixed} disabled />
              </Field>
            ) : (
              <>
                <Field label="Host nas imagens" hint="Como aparece no nome da imagem, com porta se houver.">
                  <Input value={f.host} onChange={(e) => set({ host: e.target.value.trim().toLowerCase() })} placeholder="harbor.exemplo.com" />
                </Field>
                <Field label="Endpoint (opcional)" hint="Padrão: https://<host>. Use http:// para registry sem TLS.">
                  <Input value={f.endpoint} onChange={(e) => set({ endpoint: e.target.value.trim() })} placeholder="https://harbor.exemplo.com" />
                </Field>
              </>
            )}
          </div>

          <Field
            label="Mirrors (um por linha)"
            hint="Tentados antes do endpoint para pull. Ex.: um cache local na frente do Docker Hub para fugir do rate limit. Se o mirror falhar, o containerd cai para o endpoint."
          >
            <Textarea value={f.mirrors} onChange={(e) => set({ mirrors: e.target.value })} placeholder="https://cache.interno:5000" className="min-h-16" />
          </Field>

          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Usuário">
              <Input value={f.username} autoComplete="off" onChange={(e) => set({ username: e.target.value })} placeholder="(anônimo)" />
            </Field>
            <Field label="Senha / token" hint={registry?.hasPassword ? "Deixe em branco para manter a atual." : undefined}>
              <Input
                type="password"
                value={f.password}
                autoComplete="new-password"
                onChange={(e) => set({ password: e.target.value })}
                placeholder={registry?.hasPassword ? "•••••••• (guardada)" : ""}
              />
            </Field>
          </div>
          {registry?.hasPassword && (
            <Check checked={f.clearPassword} onChange={(v) => set({ clearPassword: v })}>
              Remover a senha guardada
            </Check>
          )}

          <Field label="CA do registry (PEM, opcional)" hint="Para registries com certificado de uma CA interna. Gravado em certs.d/<host>/ca.crt nos nós.">
            <Textarea value={f.caCert} onChange={(e) => set({ caCert: e.target.value })} placeholder="-----BEGIN CERTIFICATE-----" className="min-h-16" />
          </Field>
          <Check checked={f.skipVerify} onChange={(v) => set({ skipVerify: v })}>
            Ignorar verificação TLS (skip_verify). Use só em laboratório: qualquer um no caminho pode trocar as imagens.
          </Check>

          <div className="space-y-3 rounded-md border p-3">
            <Check checked={f.pullSecret} onChange={(v) => set({ pullSecret: v })}>
              Criar imagePullSecrets nos clusters (exige usuário e senha) e adicioná-los à ServiceAccount <code>default</code> de cada namespace
            </Check>
            {f.pullSecret && (
              <Field label="Namespaces" hint="Separados por vírgula. Vazio: todos, exceto kube-system, kube-public e kube-node-lease.">
                <Input value={f.namespaces} onChange={(e) => set({ namespaces: e.target.value })} placeholder="todos" />
              </Field>
            )}
          </div>

          <div className="space-y-2">
            <div className="text-sm font-medium">Clusters</div>
            {clusters.length === 0 && <p className="text-muted-foreground text-xs">Nenhum cluster ainda: o registry vale para todos os que forem criados.</p>}
            <div className="flex flex-wrap gap-x-4 gap-y-2">
              {clusters.map((c) => (
                <Check
                  key={c.id}
                  checked={f.clusterIds.includes(c.id)}
                  onChange={(v) => set({ clusterIds: v ? [...f.clusterIds, c.id] : f.clusterIds.filter((x) => x !== c.id) })}
                >
                  {c.name}
                </Check>
              ))}
            </div>
            {clusters.length > 0 && <p className="text-muted-foreground text-xs">Nenhum marcado: vale para todos os clusters.</p>}
          </div>

          <ErrorText error={save.error} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancelar
            </Button>
            <Button requires="operator" type="submit" disabled={save.isPending}>
              {save.isPending && <Loader2 className="animate-spin" />} Salvar
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function CheckResult({ r }: { r: RegistryCheck }) {
  const Icon = r.ok ? CheckCircle2 : CircleAlert;
  return (
    <span className={"inline-flex items-center gap-1 text-xs " + (r.ok ? "text-success" : "text-destructive")}>
      <Icon className="size-3.5" /> {r.message}
    </span>
  );
}

function RegistryRow({ r, clusters, onEdit }: { r: Registry; clusters: Cluster[]; onEdit: () => void }) {
  const qc = useQueryClient();
  const check = useMutation({ mutationFn: () => api.checkRegistry(r.id) });
  const del = useMutation({ mutationFn: () => api.deleteRegistry(r.id), onSuccess: () => qc.invalidateQueries({ queryKey: ["registries"] }) });
  const names = new Map(clusters.map((c) => [c.id, c.name]));
  return (
    <TableRow>
      <TableCell>
        <div className="font-medium">{r.name}</div>
        <Badge variant="secondary" className="mt-1">
          {kindLabel[r.kind] ?? r.kind}
        </Badge>
      </TableCell>
      <TableCell className="max-w-72">
        <div className="font-mono text-xs">{r.host}</div>
        <div className="text-muted-foreground truncate font-mono text-xs" title={r.endpoint}>
          {r.endpoint}
        </div>
        {r.mirrors.map((m) => (
          <div key={m} className="text-muted-foreground truncate font-mono text-xs" title={`mirror ${m}`}>
            ↳ {m}
          </div>
        ))}
        {(r.caCert || r.skipVerify) && (
          <div className="mt-1 flex gap-1">
            {r.caCert && <Badge variant="outline">CA própria</Badge>}
            {r.skipVerify && <Badge variant="warning">sem verificação TLS</Badge>}
          </div>
        )}
      </TableCell>
      <TableCell className="text-sm">
        {r.username ? (
          <span className="inline-flex items-center gap-1">
            <KeyRound className="text-muted-foreground size-3.5" /> {r.username}
            {!r.hasPassword && <Badge variant="warning">sem senha</Badge>}
          </span>
        ) : (
          <span className="text-muted-foreground">anônimo</span>
        )}
      </TableCell>
      <TableCell className="text-sm">
        {!r.pullSecret || !r.username || !r.hasPassword ? (
          <span className="text-muted-foreground">não cria</span>
        ) : (
          <>
            <div className="font-mono text-xs">{r.secretName}</div>
            <div className="text-muted-foreground text-xs">{r.namespaces.length ? r.namespaces.join(", ") : "todos os namespaces"}</div>
          </>
        )}
      </TableCell>
      <TableCell className="text-sm">{r.clusterIds.length ? r.clusterIds.map((id) => names.get(id) ?? id.slice(0, 8)).join(", ") : "todos"}</TableCell>
      <TableCell className="text-right">
        <div className="flex justify-end gap-1">
          <Button requires="operator" variant="ghost" size="icon" title="Testar conexão e credenciais" disabled={check.isPending} onClick={() => check.mutate()}>
            {check.isPending ? <Loader2 className="animate-spin" /> : <PlugZap />}
          </Button>
          <Button requires="operator" variant="ghost" size="icon" title="Editar" onClick={onEdit}>
            <Pencil />
          </Button>
          <ConfirmButton
            variant="ghost"
            size="icon"
            destructive
            title={`Excluir o registry ${r.name}?`}
            description="A senha é apagada do cofre. Nos clusters, a configuração do containerd e os imagePullSecrets saem na próxima vez que você aplicar os registries."
            confirmLabel="Excluir"
            onConfirm={() => del.mutate()}
          >
            <Trash2 className="text-destructive" />
          </ConfirmButton>
        </div>
        {check.data && <CheckResult r={check.data} />}
        {(check.error || del.error) && <ErrorText error={check.error ?? del.error} />}
      </TableCell>
    </TableRow>
  );
}

/** Per cluster: apply the registries (containerd on every node + pull secrets). */
function ApplyCard({ clusters, jobs }: { clusters: Cluster[]; jobs: Job[] }) {
  const qc = useQueryClient();
  const sync = useMutation({ mutationFn: api.syncRegistries, onSuccess: () => qc.invalidateQueries({ queryKey: ["jobs"] }) });
  const ready = clusters.filter((c) => c.hasKubeconfig);
  return (
    <Card className="py-0">
      <CardHeader className="pt-6">
        <CardTitle>Aplicar nos clusters</CardTitle>
        <CardDescription>
          Grava <code>/etc/containerd/certs.d/&lt;host&gt;/hosts.toml</code> em cada nó, um por vez (o containerd só reinicia na primeira vez, e os
          containers continuam rodando), e cria ou remove os imagePullSecrets. Rode depois de cada mudança nos registries.
        </CardDescription>
      </CardHeader>
      <CardContent className="px-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Cluster</TableHead>
              <TableHead>Última aplicação</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {ready.map((c) => {
              const last = jobs.find((j) => j.kind === "cluster.registries-sync" && j.targetId === c.id);
              const running = last && !isFinal(last.status);
              return (
                <TableRow key={c.id}>
                  <TableCell>
                    <Link to={`/clusters/${c.id}/images`} className="font-medium hover:underline">
                      {c.name}
                    </Link>
                  </TableCell>
                  <TableCell className="text-sm">
                    {last ? (
                      <Link to={`/jobs/${last.id}`} className="inline-flex items-center gap-2 hover:underline">
                        <JobStatusBadge status={last.status} /> {formatDate(last.finishedAt ?? last.createdAt)}
                      </Link>
                    ) : (
                      <span className="text-muted-foreground">nunca</span>
                    )}
                  </TableCell>
                  <TableCell className="text-right">
                    <Button requires="operator" size="sm" variant={running ? "outline" : "default"} disabled={running || (sync.isPending && sync.variables === c.id)} onClick={() => sync.mutate(c.id)}>
                      {running ? <Loader2 className="animate-spin" /> : <UploadCloud />} {running ? "Aplicando…" : "Aplicar"}
                    </Button>
                  </TableCell>
                </TableRow>
              );
            })}
            {ready.length === 0 && (
              <TableRow>
                <TableCell colSpan={3} className="text-muted-foreground py-6 text-center text-sm">
                  Nenhum cluster provisionado ainda. Clusters novos já saem com o containerd lendo <code>certs.d</code>.
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
        {sync.error && (
          <div className="px-6 pb-4">
            <ErrorText error={sync.error} />
          </div>
        )}
      </CardContent>
    </Card>
  );
}

export function RegistriesPage() {
  const registries = useQuery({ queryKey: ["registries"], queryFn: api.registries });
  const clusters = useQuery({ queryKey: ["clusters"], queryFn: api.clusters });
  const jobs = useQuery({
    queryKey: ["jobs", "registries"],
    queryFn: () => api.jobs({ limit: 200 }),
    refetchInterval: (q) => ((q.state.data ?? []).some((j) => j.kind === "cluster.registries-sync" && !isFinal(j.status)) ? 3_000 : 20_000),
  });
  const [editing, setEditing] = useState<Registry | "new">();
  const list = registries.data ?? [];
  const cl = useMemo(() => clusters.data ?? [], [clusters.data]);

  return (
    <>
      <PageHeader
        title="Registries"
        description="De onde os clusters baixam as imagens: Docker Hub, GHCR, Quay, Harbor, registries privados e mirrors."
        actions={
          <Button requires="operator" onClick={() => setEditing("new")}>
            <Plus /> Novo registry
          </Button>
        }
      />
      <div className="space-y-6">
        <Card className="py-0">
          <CardContent className="px-0">
            {registries.isLoading ? (
              <Loader2 className="text-muted-foreground m-6 animate-spin" />
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Registry</TableHead>
                    <TableHead>Host, endpoint e mirrors</TableHead>
                    <TableHead>Credenciais</TableHead>
                    <TableHead>imagePullSecret</TableHead>
                    <TableHead>Clusters</TableHead>
                    <TableHead />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {list.map((r) => (
                    <RegistryRow key={r.id} r={r} clusters={cl} onEdit={() => setEditing(r)} />
                  ))}
                  {list.length === 0 && (
                    <TableRow>
                      <TableCell colSpan={6} className="text-muted-foreground py-8 text-center text-sm">
                        Nenhum registry cadastrado. Sem cadastro, os nós baixam direto dos registries públicos, sem credenciais nem mirrors.
                      </TableCell>
                    </TableRow>
                  )}
                </TableBody>
              </Table>
            )}
            <ErrorText error={registries.error} />
          </CardContent>
        </Card>
        <ApplyCard clusters={cl} jobs={jobs.data ?? []} />
      </div>
      {editing && <RegistryDialog registry={editing === "new" ? undefined : editing} clusters={cl} onClose={() => setEditing(undefined)} />}
    </>
  );
}
