import { useEffect, useMemo, useState } from "react";
import { useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Eye, FileCode2, Loader2, PackageOpen, Play, Plus, ShieldCheck, Trash2, X } from "lucide-react";
import { api, type AppSpec, type ApplySummary, type HelmInput, type Job } from "@/lib/api";
import { formatDate } from "@/lib/utils";
import { ClusterHeader, KubeError } from "@/components/cluster/ClusterHeader";
import { NamespacePicker, Tabs } from "@/components/cluster/NamespacePicker";
import { Empty } from "@/components/cluster/k8s";
import { ApplyResults, ConfirmButton, DeleteDialog, ErrorText, Field, JobLink, YamlEditor } from "@/components/deploy/shared";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, NativeSelect } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

type Tab = "app" | "yaml" | "helm";

const SAMPLE = `# Cole ou escreva manifests (vários documentos separados por ---).
# "Validar" roda dry-run=server: a API do cluster valida e passa pelos admission
# controllers sem gravar nada. "Aplicar" usa server-side apply.
apiVersion: v1
kind: ConfigMap
metadata:
  name: exemplo
data:
  mensagem: "olá do k8sfront"
`;

/** Registry host of an image reference, as containerd resolves it (nginx → docker.io). */
function imageHost(image: string) {
  const first = image.trim().split("/")[0] ?? "";
  return image.includes("/") && (first.includes(".") || first.includes(":") || first === "localhost") ? first.toLowerCase() : "docker.io";
}

function emptyApp(): AppSpec {
  return {
    name: "",
    namespace: "default",
    image: "",
    replicas: 1,
    containerPort: 80,
    env: [],
    healthPath: "",
    resources: { cpuRequest: "100m", memoryRequest: "128Mi", cpuLimit: "", memoryLimit: "256Mi" },
    service: { type: "ClusterIP", port: 80, nodePort: 0 },
  };
}

function Section({ title, description, children, toggle }: { title: string; description?: string; children?: React.ReactNode; toggle?: React.ReactNode }) {
  return (
    <div className="space-y-3 border-t pt-4 first:border-t-0 first:pt-0">
      <div className="flex items-start justify-between gap-3">
        <div>
          <div className="text-sm font-medium">{title}</div>
          {description && <div className="text-muted-foreground text-xs">{description}</div>}
        </div>
        {toggle}
      </div>
      {children}
    </div>
  );
}

function Toggle({ on, onChange, label }: { on: boolean; onChange: (v: boolean) => void; label: string }) {
  return (
    <label className="flex shrink-0 cursor-pointer items-center gap-2 text-sm">
      <input type="checkbox" className="accent-primary size-4" checked={on} onChange={(e) => onChange(e.target.checked)} />
      {label}
    </label>
  );
}

/** Visual form → manifests rendered by the backend → dry-run → apply. */
function AppTab({ clusterId, onOpenInEditor }: { clusterId: string; onOpenInEditor: (yaml: string) => void }) {
  const [app, setApp] = useState<AppSpec>(emptyApp);
  const [result, setResult] = useState<ApplySummary>();
  const [validated, setValidated] = useState("");
  const [deleting, setDeleting] = useState(false);
  const set = (patch: Partial<AppSpec>) => setApp((a) => ({ ...a, ...patch }));
  const key = JSON.stringify(app);

  const preview = useMutation({ mutationFn: () => api.renderApp(app) });
  const run = useMutation({
    mutationFn: (dryRun: boolean) => api.deployApp(clusterId, app, dryRun),
    onSuccess: (s) => {
      setResult(s);
      setValidated(s.dryRun && s.failed === 0 ? key : "");
    },
  });
  // Any edit invalidates the previous preview/dry-run.
  useEffect(() => {
    setValidated("");
  }, [key]);
  const num = (v: string) => (v === "" ? 0 : Number(v));
  const registries = useQuery({ queryKey: ["registries"], queryFn: api.registries });
  const withCreds = (registries.data ?? []).filter(
    (r) => r.username && r.hasPassword && (r.clusterIds.length === 0 || r.clusterIds.includes(clusterId)),
  );
  const autoRegistry = withCreds.find((r) => r.pullSecret && r.host === imageHost(app.image));

  return (
    <div className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
      <Card>
        <CardHeader>
          <CardTitle>Nova aplicação</CardTitle>
          <CardDescription>O backend gera Deployment, Service, Ingress, PVC e imagePullSecret a partir do formulário.</CardDescription>
        </CardHeader>
        <CardContent className="space-y-5">
          <Section title="Container">
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="Nome">
                <Input value={app.name} onChange={(e) => set({ name: e.target.value.toLowerCase() })} placeholder="minha-api" />
              </Field>
              <Field label="Namespace" hint="Criado se não existir.">
                <Input value={app.namespace} onChange={(e) => set({ namespace: e.target.value.toLowerCase() })} />
              </Field>
              <Field label="Imagem" className="sm:col-span-2">
                <Input value={app.image} onChange={(e) => set({ image: e.target.value.trim() })} placeholder="registry.exemplo.com/time/api:1.4.2" />
              </Field>
              <Field label="Réplicas">
                <Input type="number" min={0} max={100} value={app.replicas} onChange={(e) => set({ replicas: num(e.target.value) })} />
              </Field>
              <Field label="Porta do container" hint="0 = sem porta (sem Service).">
                <Input type="number" min={0} max={65535} value={app.containerPort} onChange={(e) => set({ containerPort: num(e.target.value) })} />
              </Field>
              <Field label="Health check HTTP (opcional)" hint="Readiness e liveness em GET neste caminho." className="sm:col-span-2">
                <Input value={app.healthPath} onChange={(e) => set({ healthPath: e.target.value.trim() })} placeholder="/healthz" />
              </Field>
            </div>
          </Section>

          <Section title="Recursos" description="Requests reservam capacidade no agendamento; limits cortam o consumo.">
            <div className="grid gap-3 sm:grid-cols-4">
              {(
                [
                  ["cpuRequest", "CPU request", "100m"],
                  ["memoryRequest", "Memória request", "128Mi"],
                  ["cpuLimit", "CPU limit", "—"],
                  ["memoryLimit", "Memória limit", "256Mi"],
                ] as const
              ).map(([k, label, ph]) => (
                <Field key={k} label={label}>
                  <Input value={app.resources[k]} placeholder={ph} onChange={(e) => set({ resources: { ...app.resources, [k]: e.target.value.trim() } })} />
                </Field>
              ))}
            </div>
          </Section>

          <Section
            title="Variáveis de ambiente"
            description="Valores literais. Para segredos, use um Secret pelo editor YAML."
            toggle={
              <Button variant="ghost" size="sm" onClick={() => set({ env: [...app.env, { name: "", value: "" }] })}>
                <Plus /> Adicionar
              </Button>
            }
          >
            {app.env.map((e, i) => (
              <div key={i} className="flex gap-2">
                <Input
                  className="font-mono"
                  placeholder="NOME"
                  value={e.name}
                  onChange={(ev) => set({ env: app.env.map((x, j) => (j === i ? { ...x, name: ev.target.value } : x)) })}
                />
                <Input placeholder="valor" value={e.value} onChange={(ev) => set({ env: app.env.map((x, j) => (j === i ? { ...x, value: ev.target.value } : x)) })} />
                <Button variant="ghost" size="icon" aria-label="Remover" onClick={() => set({ env: app.env.filter((_, j) => j !== i) })}>
                  <X />
                </Button>
              </div>
            ))}
          </Section>

          <Section
            title="Service"
            toggle={<Toggle label="Expor" on={!!app.service} onChange={(on) => set({ service: on ? { type: "ClusterIP", port: app.containerPort || 80, nodePort: 0 } : undefined, ingress: on ? app.ingress : undefined })} />}
          >
            {app.service && (
              <div className="grid gap-3 sm:grid-cols-3">
                <Field label="Tipo">
                  <NativeSelect value={app.service.type} onChange={(e) => set({ service: { ...app.service!, type: e.target.value as "ClusterIP" } })}>
                    <option value="ClusterIP">ClusterIP</option>
                    <option value="NodePort">NodePort</option>
                    <option value="LoadBalancer">LoadBalancer</option>
                  </NativeSelect>
                </Field>
                <Field label="Porta">
                  <Input type="number" value={app.service.port} onChange={(e) => set({ service: { ...app.service!, port: num(e.target.value) } })} />
                </Field>
                {app.service.type !== "ClusterIP" && (
                  <Field label="nodePort" hint="30000-32767; 0 = automático">
                    <Input type="number" value={app.service.nodePort} onChange={(e) => set({ service: { ...app.service!, nodePort: num(e.target.value) } })} />
                  </Field>
                )}
              </div>
            )}
          </Section>

          {app.service && (
            <Section
              title="Ingress"
              description="Exige um ingress controller no cluster."
              toggle={<Toggle label="Publicar" on={!!app.ingress} onChange={(on) => set({ ingress: on ? { host: "", path: "/", className: "", tlsSecret: "" } : undefined })} />}
            >
              {app.ingress && (
                <div className="grid gap-3 sm:grid-cols-2">
                  <Field label="Host">
                    <Input value={app.ingress.host} placeholder="app.exemplo.com" onChange={(e) => set({ ingress: { ...app.ingress!, host: e.target.value.trim() } })} />
                  </Field>
                  <Field label="Caminho">
                    <Input value={app.ingress.path} onChange={(e) => set({ ingress: { ...app.ingress!, path: e.target.value.trim() } })} />
                  </Field>
                  <Field label="ingressClassName (opcional)">
                    <Input value={app.ingress.className} placeholder="nginx" onChange={(e) => set({ ingress: { ...app.ingress!, className: e.target.value.trim() } })} />
                  </Field>
                  <Field label="Secret TLS (opcional)">
                    <Input value={app.ingress.tlsSecret} placeholder="app-tls" onChange={(e) => set({ ingress: { ...app.ingress!, tlsSecret: e.target.value.trim() } })} />
                  </Field>
                </div>
              )}
            </Section>
          )}

          <Section
            title="Volume persistente"
            toggle={<Toggle label="Usar PVC" on={!!app.volume} onChange={(on) => set({ volume: on ? { size: "1Gi", storageClass: "", mountPath: "/data", accessMode: "ReadWriteOnce" } : undefined })} />}
          >
            {app.volume && (
              <div className="grid gap-3 sm:grid-cols-2">
                <Field label="Tamanho">
                  <Input value={app.volume.size} onChange={(e) => set({ volume: { ...app.volume!, size: e.target.value.trim() } })} />
                </Field>
                <Field label="Montar em">
                  <Input value={app.volume.mountPath} onChange={(e) => set({ volume: { ...app.volume!, mountPath: e.target.value.trim() } })} />
                </Field>
                <Field label="StorageClass" hint="Vazio = a padrão do cluster.">
                  <Input value={app.volume.storageClass} onChange={(e) => set({ volume: { ...app.volume!, storageClass: e.target.value.trim() } })} />
                </Field>
                <Field label="Modo de acesso" hint="RWO usa a estratégia Recreate.">
                  <NativeSelect value={app.volume.accessMode} onChange={(e) => set({ volume: { ...app.volume!, accessMode: e.target.value } })}>
                    <option>ReadWriteOnce</option>
                    <option>ReadWriteMany</option>
                    <option>ReadOnlyMany</option>
                    <option>ReadWriteOncePod</option>
                  </NativeSelect>
                </Field>
              </div>
            )}
          </Section>

          <Section
            title="Registry privado"
            description="Use um registry cadastrado (o Secret compartilhado k8sfront-registry-<nome> é criado no namespace) ou credenciais avulsas. Senhas nunca aparecem no preview nem nos logs."
            toggle={
              <Toggle
                label="Usar"
                on={!!app.pullSecret}
                onChange={(on) =>
                  set({
                    pullSecret: on
                      ? { name: "", server: "", username: "", password: "", registryId: autoRegistry?.id ?? withCreds[0]?.id }
                      : undefined,
                  })
                }
              />
            }
          >
            {!app.pullSecret && autoRegistry && (
              <p className="text-muted-foreground text-xs">
                A imagem é de <span className="font-mono">{autoRegistry.host}</span>: o registry cadastrado <b>{autoRegistry.name}</b> será usado automaticamente.
              </p>
            )}
            {app.pullSecret && (
              <div className="grid gap-3 sm:grid-cols-2">
                <Field label="Origem das credenciais" className="sm:col-span-2">
                  <NativeSelect
                    value={app.pullSecret.registryId ?? ""}
                    onChange={(e) => set({ pullSecret: { name: "", server: "", username: "", password: "", registryId: e.target.value || undefined } })}
                  >
                    {withCreds.map((r) => (
                      <option key={r.id} value={r.id}>
                        Registry cadastrado: {r.name} ({r.host})
                      </option>
                    ))}
                    <option value="">Credenciais avulsas / Secret existente</option>
                  </NativeSelect>
                </Field>
                {!app.pullSecret.registryId && (
                  <>
                    <Field label="Secret existente (opcional)" hint="Preencha só este campo para reaproveitar um Secret.">
                      <Input value={app.pullSecret.name} placeholder={`${app.name || "app"}-registry`} onChange={(e) => set({ pullSecret: { ...app.pullSecret!, name: e.target.value.trim() } })} />
                    </Field>
                    <Field label="Servidor">
                      <Input value={app.pullSecret.server} placeholder="registry.exemplo.com" onChange={(e) => set({ pullSecret: { ...app.pullSecret!, server: e.target.value.trim() } })} />
                    </Field>
                    <Field label="Usuário">
                      <Input value={app.pullSecret.username} autoComplete="off" onChange={(e) => set({ pullSecret: { ...app.pullSecret!, username: e.target.value } })} />
                    </Field>
                    <Field label="Senha / token">
                      <Input type="password" value={app.pullSecret.password} autoComplete="new-password" onChange={(e) => set({ pullSecret: { ...app.pullSecret!, password: e.target.value } })} />
                    </Field>
                  </>
                )}
              </div>
            )}
          </Section>
        </CardContent>
      </Card>

      <div className="min-w-0 space-y-4">
        <Card>
          <CardHeader>
            <CardTitle>Manifests gerados</CardTitle>
            <CardDescription>Pré-visualize, valide no servidor (dry-run) e só então aplique.</CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            <div className="flex flex-wrap gap-2">
              <Button requires="operator" variant="outline" onClick={() => preview.mutate()} disabled={preview.isPending}>
                {preview.isPending ? <Loader2 className="animate-spin" /> : <Eye />} Pré-visualizar YAML
              </Button>
              <Button requires="operator" variant="outline" onClick={() => run.mutate(true)} disabled={run.isPending}>
                {run.isPending && run.variables === true ? <Loader2 className="animate-spin" /> : <ShieldCheck />} Validar (dry-run)
              </Button>
              <ConfirmButton
                variant="default"
                size="default"
                disabled={validated !== key || run.isPending}
                title={`Aplicar ${app.name} em ${app.namespace}?`}
                description={<p>Os objetos validados no dry-run serão aplicados no cluster com server-side apply.</p>}
                confirmLabel="Aplicar"
                onConfirm={() => run.mutate(false)}
              >
                <Play /> Aplicar
              </ConfirmButton>
              {preview.data && (
                <Button variant="ghost" onClick={() => onOpenInEditor(preview.data!.yaml)}>
                  <FileCode2 /> Abrir no editor YAML
                </Button>
              )}
              <Button requires="operator" variant="ghost" className="text-destructive ml-auto" disabled={!app.name || !app.image} onClick={() => setDeleting(true)}>
                <Trash2 /> Excluir aplicação
              </Button>
            </div>
            <DeleteDialog
              clusterId={clusterId}
              request={{ app }}
              title={`Excluir a aplicação ${app.name} de ${app.namespace || "default"}?`}
              confirmWord={app.name}
              open={deleting}
              onOpenChange={setDeleting}
            />
            {validated !== key && <p className="text-muted-foreground text-xs">“Aplicar” libera depois de um dry-run sem erros do formulário atual.</p>}
            <ErrorText error={preview.error ?? run.error} />
            {preview.data && <YamlEditor value={preview.data.yaml} readOnly height={420} />}
          </CardContent>
        </Card>
        {result && (
          <Card>
            <CardHeader>
              <CardTitle>{result.dryRun ? "Resultado do dry-run" : "Resultado do apply"}</CardTitle>
            </CardHeader>
            <CardContent>
              <ApplyResults summary={result} />
            </CardContent>
          </Card>
        )}
      </div>
    </div>
  );
}

/** Monaco editor with dry-run=server before server-side apply. */
function YamlTab({ clusterId, text, setText }: { clusterId: string; text: string; setText: (v: string) => void }) {
  const [ns, setNs] = useState("");
  const [result, setResult] = useState<ApplySummary>();
  const [validated, setValidated] = useState("");
  const [deleting, setDeleting] = useState(false);
  const [keepNs, setKeepNs] = useState(false);
  const key = ns + "\u0000" + text;
  const run = useMutation({
    mutationFn: (dryRun: boolean) => api.applyManifest(clusterId, text, ns, dryRun),
    onSuccess: (s) => {
      setResult(s);
      setValidated(s.dryRun && s.failed === 0 ? key : "");
    },
  });
  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle>Editor YAML</CardTitle>
          <CardDescription>Objetos sem namespace vão para o namespace escolhido (ou default). Namespaces e CRDs são aplicados primeiro.</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="flex flex-wrap items-center gap-2">
            <NamespacePicker clusterId={clusterId} value={ns} onChange={setNs} />
            <Button requires="operator" variant="outline" onClick={() => run.mutate(true)} disabled={run.isPending || !text.trim()}>
              {run.isPending ? <Loader2 className="animate-spin" /> : <ShieldCheck />} Validar (dry-run)
            </Button>
            <ConfirmButton
              variant="default"
              size="default"
              disabled={validated !== key || run.isPending}
              title="Aplicar manifests?"
              description={<p>Os {result?.results.length ?? ""} objeto(s) validados serão aplicados com server-side apply (field manager k8sfront, force).</p>}
              confirmLabel="Aplicar"
              onConfirm={() => run.mutate(false)}
            >
              <Play /> Aplicar
            </ConfirmButton>
            {validated !== key && <span className="text-muted-foreground text-xs">Aplicar libera após um dry-run sem erros deste conteúdo.</span>}
            <div className="ml-auto flex items-center gap-3">
              <Toggle on={keepNs} onChange={setKeepNs} label="Manter namespaces" />
              <Button requires="operator" variant="ghost" className="text-destructive" disabled={!text.trim()} onClick={() => setDeleting(true)}>
                <Trash2 /> Excluir objetos do manifesto
              </Button>
            </div>
          </div>
          <DeleteDialog
            clusterId={clusterId}
            request={{ manifest: text, namespace: ns, keepNamespaces: keepNs }}
            title="Excluir os objetos deste manifesto?"
            confirmWord="excluir"
            open={deleting}
            onOpenChange={setDeleting}
          />
          <ErrorText error={run.error} />
          <YamlEditor value={text} onChange={setText} height={520} />
        </CardContent>
      </Card>
      {result && (
        <Card>
          <CardHeader>
            <CardTitle>{result.dryRun ? "Resultado do dry-run" : "Resultado do apply"}</CardTitle>
          </CardHeader>
          <CardContent>
            <ApplyResults summary={result} />
          </CardContent>
        </Card>
      )}
    </div>
  );
}

const releaseVariant = (s: string) => (s === "deployed" ? "success" : s.startsWith("pending") ? "warning" : s === "failed" ? "destructive" : "secondary");

function HelmTab({ clusterId }: { clusterId: string }) {
  const qc = useQueryClient();
  const releases = useQuery({ queryKey: ["helm", clusterId], queryFn: () => api.helmReleases(clusterId), refetchInterval: 10_000 });
  const [form, setForm] = useState({ release: "", namespace: "default", repo: "", chart: "", version: "", values: "# values.yaml (opcional)\n" });
  const [job, setJob] = useState<Job>();
  const submit = useMutation({
    mutationFn: (input: HelmInput) => api.helm(clusterId, input),
    onSuccess: (j) => {
      setJob(j);
      qc.invalidateQueries({ queryKey: ["helm", clusterId] });
    },
  });
  const valuesBody = useMemo(() => form.values.split("\n").filter((l) => !l.trim().startsWith("#")).join("\n").trim(), [form.values]);

  return (
    <div className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
      <Card className="min-w-0">
        <CardHeader>
          <CardTitle>Releases Helm</CardTitle>
          <CardDescription>Lidos dos metadados dos Secrets do Helm; o conteúdo (values) nunca é lido.</CardDescription>
        </CardHeader>
        <CardContent className="px-0">
          {releases.error ? (
            <div className="px-6">
              <KubeError error={releases.error} />
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Release</TableHead>
                  <TableHead>Revisão</TableHead>
                  <TableHead>Estado</TableHead>
                  <TableHead>Atualizado</TableHead>
                  <TableHead />
                </TableRow>
              </TableHeader>
              <TableBody>
                {(releases.data ?? []).map((r) => (
                  <TableRow key={`${r.namespace}/${r.name}`}>
                    <TableCell>
                      <div className="font-medium">{r.name}</div>
                      <div className="text-muted-foreground text-xs">{r.namespace}</div>
                    </TableCell>
                    <TableCell className="tabular-nums">{r.revision}</TableCell>
                    <TableCell>
                      <Badge variant={releaseVariant(r.status)}>{r.status}</Badge>
                    </TableCell>
                    <TableCell className="text-sm">{formatDate(r.updated)}</TableCell>
                    <TableCell className="text-right">
                      <ConfirmButton
                        variant="ghost"
                        size="icon"
                        destructive
                        title={`Desinstalar ${r.name}?`}
                        description={<p>Roda “helm uninstall {r.name} -n {r.namespace}” no control plane primário. Recursos do chart serão removidos.</p>}
                        confirmLabel="Desinstalar"
                        onConfirm={() => submit.mutate({ action: "uninstall", release: r.name, namespace: r.namespace })}
                      >
                        <Trash2 />
                      </ConfirmButton>
                    </TableCell>
                  </TableRow>
                ))}
                {!releases.isLoading && (releases.data ?? []).length === 0 && <Empty cols={5} />}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
      <Card className="min-w-0">
        <CardHeader>
          <CardTitle>Instalar ou atualizar chart</CardTitle>
          <CardDescription>
            “helm upgrade --install --wait” executado no control plane primário (Helm instalado com checksum verificado). Values vão cifrados até o job terminar.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Release">
              <Input value={form.release} placeholder="ingress-nginx" onChange={(e) => setForm({ ...form, release: e.target.value.toLowerCase() })} />
            </Field>
            <Field label="Namespace" hint="Criado se não existir.">
              <Input value={form.namespace} onChange={(e) => setForm({ ...form, namespace: e.target.value.toLowerCase() })} />
            </Field>
            <Field label="Repositório" hint="URL https:// (vazio para oci://)" className="sm:col-span-2">
              <Input value={form.repo} placeholder="https://kubernetes.github.io/ingress-nginx" onChange={(e) => setForm({ ...form, repo: e.target.value.trim() })} />
            </Field>
            <Field label="Chart">
              <Input value={form.chart} placeholder="ingress-nginx ou oci://…" onChange={(e) => setForm({ ...form, chart: e.target.value.trim() })} />
            </Field>
            <Field label="Versão (opcional)">
              <Input value={form.version} placeholder="mais recente" onChange={(e) => setForm({ ...form, version: e.target.value.trim() })} />
            </Field>
          </div>
          <YamlEditor value={form.values} onChange={(v) => setForm({ ...form, values: v })} height={240} />
          <div className="flex flex-wrap items-center gap-2">
            <Button requires="operator"
              disabled={submit.isPending || !form.release || !form.chart}
              onClick={() =>
                submit.mutate({ action: "install", release: form.release, namespace: form.namespace, repo: form.repo, chart: form.chart, version: form.version, values: valuesBody ? form.values : "" })
              }
            >
              {submit.isPending ? <Loader2 className="animate-spin" /> : <PackageOpen />} Instalar / atualizar
            </Button>
          </div>
          <ErrorText error={submit.error} />
          {job && <JobLink job={job} text={`Job Helm enviado (${job.id.slice(0, 8)})`} />}
        </CardContent>
      </Card>
    </div>
  );
}

export function ClusterDeployPage() {
  const { id = "" } = useParams();
  const [tab, setTab] = useState<Tab>("app");
  const [yaml, setYaml] = useState(SAMPLE);
  return (
    <>
      <ClusterHeader id={id} />
      <div className="mb-4">
        <Tabs
          value={tab}
          onChange={setTab}
          items={[
            { id: "app", label: "Aplicação (formulário)" },
            { id: "yaml", label: "Editor YAML" },
            { id: "helm", label: "Helm" },
          ]}
        />
      </div>
      <div className={tab === "app" ? "" : "hidden"}>
        <AppTab
          clusterId={id}
          onOpenInEditor={(y) => {
            setYaml(y);
            setTab("yaml");
          }}
        />
      </div>
      {tab === "yaml" && <YamlTab clusterId={id} text={yaml} setText={setYaml} />}
      {tab === "helm" && <HelmTab clusterId={id} />}
    </>
  );
}
