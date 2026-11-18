import { useMemo, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowRight,
  Ban,
  BadgeCheck,
  CircleSlash,
  KeyRound,
  Loader2,
  PackageCheck,
  Pause,
  Play,
  Plus,
  Power,
  RefreshCcw,
  RotateCw,
  Search,
  ShieldAlert,
  Trash2,
  Waves,
} from "lucide-react";
import {
  api,
  ApiError,
  endpointMode,
  isFinal,
  jobKindLabel,
  type CertsResult,
  type Cluster,
  type EndpointMode,
  type Job,
  type OSNodeReport,
  type RebootPolicy,
  type Server,
  type UpgradePlan,
} from "@/lib/api";
import { cn, duration, formatDate } from "@/lib/utils";
import { ClusterHeader, KubeError } from "@/components/cluster/ClusterHeader";
import { ConfirmButton, ErrorText, JobLink } from "@/components/deploy/shared";
import { CheckIcon, JobStatusBadge } from "@/components/status";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { NativeSelect } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

const lifecycleKinds = new Set(["cluster.upgrade", "cluster.node-add", "cluster.node-remove", "cluster.node-drain", "cluster.os-check", "cluster.os-update", "cluster.rolling-reboot", "cluster.certs", "cluster.loadbalancer"]);

function useClusterJobs(id: string) {
  return useQuery({
    queryKey: ["jobs", "cluster", id],
    queryFn: () => api.jobs({ targetId: id, limit: 100 }),
    refetchInterval: (q) => ((q.state.data ?? []).some((j) => !isFinal(j.status)) ? 3_000 : 15_000),
  });
}

function latest(jobs: Job[] | undefined, kind: string, pred?: (j: Job) => boolean) {
  return (jobs ?? []).find((j) => j.kind === kind && (!pred || pred(j)));
}

// --- Upgrade -------------------------------------------------------------------

function UpgradeCard({ id, plan, jobs }: { id: string; plan: UpgradePlan; jobs?: Job[] }) {
  const qc = useQueryClient();
  const [target, setTarget] = useState("");
  const chosen = plan.targets.find((t) => t.version === target) ?? plan.targets.find((t) => t.latest);
  const start = useMutation({
    mutationFn: (v: string) => api.startUpgrade(id, v),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["upgradePlan", id] }),
  });
  const retry = useMutation({ mutationFn: api.retryJob, onSuccess: () => qc.invalidateQueries() });
  const cancel = useMutation({ mutationFn: api.cancelJob, onSuccess: () => qc.invalidateQueries() });
  const paused = plan.pausedJob;
  const lastDone = latest(jobs, "cluster.upgrade", (j) => j.status === "succeeded");

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <RefreshCcw className="text-primary size-4" /> Upgrade do Kubernetes
        </CardTitle>
        <CardDescription>
          kubeadm + pacotes de pkgs.k8s.io, um minor por vez. Snapshot do etcd antes de cada salto; nós atualizados um por vez (cordon → drain → upgrade → Ready
          → uncordon); qualquer falha pausa o job antes do próximo nó.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-5">
        <div className="flex flex-wrap items-center gap-6">
          <div>
            <div className="text-muted-foreground text-xs">Versão da API</div>
            <div className="font-mono text-2xl font-semibold">{plan.serverVersion}</div>
          </div>
          {chosen && (
            <>
              <ArrowRight className="text-muted-foreground" />
              <div>
                <div className="text-muted-foreground text-xs">Destino</div>
                <div className="text-primary font-mono text-2xl font-semibold">{chosen.version}</div>
              </div>
            </>
          )}
          {lastDone && <div className="text-muted-foreground ml-auto text-xs">Último upgrade concluído {formatDate(lastDone.finishedAt)}</div>}
        </div>

        {paused && (
          <div
            className={cn(
              "flex flex-wrap items-center gap-2 rounded-md border px-3 py-2 text-sm",
              plan.inProgress ? "border-primary/40 bg-primary/5" : "border-warning/50 bg-warning/10",
            )}
          >
            {plan.inProgress ? <Loader2 className="text-primary size-4 animate-spin" /> : <Pause className="text-warning size-4" />}
            <span>
              Upgrade <JobStatusBadge status={paused.status} />{" "}
              {plan.inProgress ? (
                <span className="text-muted-foreground">— um nó por vez; acompanhe no console</span>
              ) : (
                paused.error && <span className="text-muted-foreground">— {paused.error}</span>
              )}
            </span>
            <div className="ml-auto flex gap-2">
              <Button size="sm" variant="outline" asChild>
                <Link to={`/jobs/${paused.id}`}>Console</Link>
              </Button>
              {(paused.status === "paused" || paused.status === "failed") && (
                <>
                  <Button requires="operator" size="sm" onClick={() => retry.mutate(paused.id)} disabled={retry.isPending}>
                    <RotateCw /> Retomar
                  </Button>
                  <Button requires="operator" size="sm" variant="ghost" onClick={() => cancel.mutate(paused.id)} disabled={cancel.isPending}>
                    <Ban /> Abortar
                  </Button>
                </>
              )}
            </div>
          </div>
        )}

        <div className="grid gap-4 lg:grid-cols-2">
          <div>
            <div className="mb-2 text-sm font-medium">Pré-checagens</div>
            <ul className="space-y-2">
              {plan.checks.map((c) => (
                <li key={c.id} className="flex gap-2 text-sm">
                  <CheckIcon status={c.status} />
                  <div>
                    <div className="leading-tight">{c.name}</div>
                    <div className="text-muted-foreground text-xs">{c.detail}</div>
                  </div>
                </li>
              ))}
            </ul>
            <p className="text-muted-foreground mt-3 text-xs">
              No job também rodam: saúde do etcd (todos os membros), uso de APIs depreciadas e `kubeadm upgrade plan`.
            </p>
          </div>
          <div>
            <div className="mb-2 text-sm font-medium">Destinos possíveis</div>
            {plan.targets.length === 0 ? (
              <p className="text-muted-foreground flex items-center gap-2 text-sm">
                <BadgeCheck className="text-success size-4" /> O cluster já está na versão estável mais recente.
              </p>
            ) : (
              <div className="space-y-2">
                {plan.targets.map((t) => (
                  <label
                    key={t.version}
                    className={cn(
                      "flex cursor-pointer items-center gap-3 rounded-md border px-3 py-2 text-sm",
                      chosen?.version === t.version && "border-primary bg-primary/5",
                    )}
                  >
                    <input type="radio" className="accent-primary" checked={chosen?.version === t.version} onChange={() => setTarget(t.version)} />
                    <span className="font-mono font-medium">{t.version}</span>
                    <Badge variant={t.kind === "patch" ? "secondary" : "info"}>{t.kind === "patch" ? "patch" : "minor"}</Badge>
                    {t.latest && <Badge variant="success">mais recente</Badge>}
                    <span className="text-muted-foreground ml-auto truncate font-mono text-xs">
                      {[plan.serverVersion, ...t.hops].join(" → ")}
                    </span>
                  </label>
                ))}
              </div>
            )}
            {chosen && (
              <div className="mt-3 flex items-center gap-2">
                <ConfirmButton
                  variant="default"
                  size="default"
                  disabled={plan.blocked || start.isPending}
                  title={`Atualizar para ${chosen.version}?`}
                  description={
                    <>
                      <p>
                        Saltos: <span className="font-mono">{[plan.serverVersion, ...chosen.hops].join(" → ")}</span> ({plan.nodes.length} nó(s), um por vez).
                      </p>
                      <p>Os pods de cada nó serão drenados respeitando PodDisruptionBudgets. Se algo falhar, o job pausa e você decide retomar ou abortar.</p>
                    </>
                  }
                  confirmLabel="Iniciar upgrade"
                  onConfirm={() => start.mutate(chosen.version)}
                >
                  {start.isPending ? <Loader2 className="animate-spin" /> : <Play />} Iniciar upgrade
                </ConfirmButton>
                {plan.blocked &&
                  (plan.inProgress ? (
                    <span className="text-muted-foreground text-xs">Já existe um upgrade em andamento; acompanhe pelo console acima.</span>
                  ) : paused ? (
                    <span className="text-warning text-xs">Retome ou aborte o upgrade interrompido acima antes de iniciar outro.</span>
                  ) : (
                    <span className="text-destructive text-xs">Resolva as pré-checagens com falha.</span>
                  ))}
              </div>
            )}
            <ErrorText error={start.error} />
            {start.data && !paused && <div className="mt-2"><JobLink job={start.data} /></div>}
          </div>
        </div>
      </CardContent>
    </Card>
  );
}

// --- Nodes ---------------------------------------------------------------------

type AddRole = "control-plane" | "worker";

function AddNodeDialog({
  id,
  role,
  cpCount,
  mode,
  onOpenChange,
  onJob,
}: {
  id: string;
  role?: AddRole;
  cpCount: number;
  mode: EndpointMode;
  onOpenChange: (v: boolean) => void;
  onJob: (j: Job) => void;
}) {
  const open = !!role;
  const servers = useQuery({ queryKey: ["servers"], queryFn: api.servers, enabled: open });
  const free = (servers.data ?? []).filter((s) => !s.clusterId);
  const [sel, setSel] = useState("");
  const qc = useQueryClient();
  const add = useMutation({
    mutationFn: () => api.addNode(id, sel, role ?? "worker"),
    onSuccess: (j) => {
      onJob(j);
      onOpenChange(false);
      setSel("");
      qc.invalidateQueries();
    },
  });
  const chosen = free.find((s) => s.id === sel);
  const cp = role === "control-plane";
  const after = cpCount + 1;
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{cp ? "Adicionar control plane" : "Adicionar worker"}</DialogTitle>
          <DialogDescription>
            {cp
              ? mode === "local-proxy"
                ? "O servidor é preparado (inclusive o proxy local da API), o proxy de todos os nós passa a incluí-lo, recebe credenciais de join e certificados do control plane (curta duração) e entra como membro do etcd."
                : "O servidor é preparado, recebe credenciais de join e certificados do control plane (curta duração), entra como membro do etcd e passa a disputar o VIP do kube-vip."
              : "O servidor é preparado (swap, kernel, containerd, pacotes na versão do cluster), recebe credenciais de join de curta duração e entra como worker."}
          </DialogDescription>
        </DialogHeader>
        {free.length === 0 ? (
          <p className="text-muted-foreground text-sm">
            Nenhum servidor livre. Cadastre um em <Link className="text-primary hover:underline" to="/servers">Servidores</Link>.
          </p>
        ) : (
          <NativeSelect value={sel} onChange={(e) => setSel(e.target.value)}>
            <option value="">Escolha um servidor…</option>
            {free.map((s) => (
              <option key={s.id} value={s.id}>
                {s.name} ({s.host}) {s.lastCheckStatus ? `· pré-checagem ${s.lastCheckStatus}` : "· sem pré-checagem"}
              </option>
            ))}
          </NativeSelect>
        )}
        {chosen && chosen.lastCheckStatus !== "pass" && chosen.lastCheckStatus !== "warn" && (
          <p className="text-warning text-xs">Recomendado rodar a pré-checagem do servidor antes (Servidores → Testar).</p>
        )}
        {cp && (
          <p className={cn("text-xs", after % 2 === 0 ? "text-warning" : "text-muted-foreground")}>
            {after % 2 === 0
              ? `O cluster ficará com ${after} control planes. Com número par o etcd tolera as mesmas falhas que com ${after - 1}: adicione mais um em seguida para chegar a ${after + 1}.`
              : `O cluster ficará com ${after} control planes e tolera a falha de ${(after - 1) / 2}.`}
          </p>
        )}
        <ErrorText error={add.error} />
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Cancelar
          </Button>
          <Button requires="operator" disabled={!sel || add.isPending} onClick={() => add.mutate()}>
            {add.isPending ? <Loader2 className="animate-spin" /> : <Plus />} Adicionar
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function removeDescription(controlPlane: boolean, cpCount: number, purge: boolean, setPurge: (v: boolean) => void) {
  const left = cpCount - 1;
  return (
    <div className="space-y-3">
      {controlPlane ? (
        <p>
          Drain → proxy local dos outros nós deixa de usá-lo → kubeadm reset (sai do etcd) → conferência do membro do etcd a partir de outro control plane →
          kubectl delete node.
        </p>
      ) : (
        <p>Drain → kubectl delete node → kubeadm reset no servidor.</p>
      )}
      {controlPlane && (
        <p className={left === 1 || left % 2 === 0 ? "text-warning" : undefined}>
          Restam {left} control plane(s)
          {left === 1 ? ", sem tolerância a falhas." : left % 2 === 0 ? `: com número par o etcd tolera as mesmas falhas que com ${left - 1}.` : "."}
        </p>
      )}
      <label className="text-foreground flex items-start gap-2">
        <input type="checkbox" className="mt-0.5" checked={purge} onChange={(e) => setPurge(e.target.checked)} />
        <span>
          Apagar também do inventário de servidores
          <span className="text-muted-foreground block text-xs">
            {purge ? "O servidor some de todas as listas ao fim do job." : "O servidor volta ao inventário como livre, para ser usado de novo."}
          </span>
        </span>
      </label>
    </div>
  );
}

function NodesCard({ id, plan, members, cluster }: { id: string; plan: UpgradePlan; members: Server[]; cluster?: Cluster }) {
  const qc = useQueryClient();
  const [job, setJob] = useState<Job>();
  const [adding, setAdding] = useState<AddRole>();
  const mode = cluster ? endpointMode(cluster.options) : "direct";
  const cpCount = members.filter((m) => m.role === "control-plane").length;
  const canAddCP = cluster?.topology === "multi-node" && mode !== "direct";
  const refresh = () => qc.invalidateQueries({ queryKey: ["upgradePlan", id] });
  const cordon = useMutation({ mutationFn: (n: string) => api.cordon(id, n), onSuccess: refresh });
  const uncordon = useMutation({ mutationFn: (n: string) => api.uncordon(id, n), onSuccess: refresh });
  const drain = useMutation({ mutationFn: (n: string) => api.drain(id, n), onSuccess: setJob });
  const [purge, setPurge] = useState(true);
  const remove = useMutation({
    mutationFn: (sid: string) => api.removeNode(id, sid, purge),
    onSuccess: (j) => {
      setJob(j);
      qc.invalidateQueries({ queryKey: ["servers"] });
    },
  });
  const byIP = new Map(members.map((m) => [m.host, m]));
  const err = cordon.error ?? uncordon.error ?? drain.error ?? remove.error;

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Waves className="text-primary size-4" /> Nós
        </CardTitle>
        <CardDescription>
          Cordon/uncordon imediatos; drain usa a Eviction API (respeita PDBs, ignora DaemonSets). Control planes: mantenha um número ímpar (3 ou 5) por causa do
          quórum do etcd.
          {mode === "direct" && " Este cluster usa o IP do primeiro control plane como endpoint, então não recebe novos control planes."}
        </CardDescription>
        <CardAction className="flex gap-2">
          {canAddCP && (
            <Button requires="operator" size="sm" variant="outline" onClick={() => setAdding("control-plane")}>
              <Plus /> Adicionar control plane
            </Button>
          )}
          <Button requires="operator" size="sm" variant="outline" onClick={() => setAdding("worker")}>
            <Plus /> Adicionar worker
          </Button>
        </CardAction>
      </CardHeader>
      <CardContent className="space-y-3 px-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Nó</TableHead>
              <TableHead>Estado</TableHead>
              <TableHead>kubelet</TableHead>
              <TableHead>SO / kernel</TableHead>
              <TableHead>Runtime</TableHead>
              <TableHead className="text-right">Ações</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {plan.nodes.map((n) => {
              const srv = byIP.get(n.internalIp);
              return (
                <TableRow key={n.name}>
                  <TableCell>
                    <div className="font-medium">{n.name}</div>
                    <div className="text-muted-foreground text-xs">
                      {n.controlPlane ? "Control Plane" : "Worker"} · {n.internalIp}
                      {srv && srv.name !== n.name && ` · ${srv.name}`}
                    </div>
                  </TableCell>
                  <TableCell>
                    <Badge variant={!n.ready ? "destructive" : n.unschedulable ? "warning" : "success"}>
                      {!n.ready ? "NotReady" : n.unschedulable ? "Cordoned" : "Pronto"}
                    </Badge>
                  </TableCell>
                  <TableCell className="font-mono text-xs">{n.kubeletVersion}</TableCell>
                  <TableCell className="text-xs">
                    <div>{n.osImage}</div>
                    <div className="text-muted-foreground">{n.kernelVersion}</div>
                  </TableCell>
                  <TableCell className="text-xs">{n.containerRuntime}</TableCell>
                  <TableCell>
                    {/* Fixed-width slots keep the buttons aligned across rows. */}
                    <div className="flex items-center justify-end gap-1">
                      {n.unschedulable ? (
                        <Button requires="operator" size="sm" variant="ghost" className="w-28 justify-start" onClick={() => uncordon.mutate(n.name)} disabled={uncordon.isPending}>
                          <Play /> Uncordon
                        </Button>
                      ) : (
                        <Button requires="operator" size="sm" variant="ghost" className="w-28 justify-start" onClick={() => cordon.mutate(n.name)} disabled={cordon.isPending}>
                          <CircleSlash /> Cordon
                        </Button>
                      )}
                      <ConfirmButton
                        variant="ghost"
                        title={`Drenar ${n.name}?`}
                        description={<p>O nó fica sem agendamento e os pods são despejados respeitando PodDisruptionBudgets (dados em emptyDir são descartados). Use Uncordon depois.</p>}
                        confirmLabel="Drenar"
                        onConfirm={() => drain.mutate(n.name)}
                      >
                        <Waves /> Drain
                      </ConfirmButton>
                      <div className="flex w-9 justify-center">
                      {srv && (!n.controlPlane || cpCount > 1) && (
                        <ConfirmButton
                          variant="ghost"
                          destructive
                          title={`Remover ${n.controlPlane ? "o control plane " : ""}${n.name} do cluster?`}
                          description={removeDescription(n.controlPlane, cpCount, purge, setPurge)}
                          confirmLabel="Remover"
                          onConfirm={() => remove.mutate(srv.id)}
                        >
                          <Trash2 />
                        </ConfirmButton>
                      )}
                      </div>
                    </div>
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
        <div className="space-y-2 px-6">
          <ErrorText error={err} />
          {job && <JobLink job={job} />}
        </div>
      </CardContent>
      <AddNodeDialog id={id} role={adding} cpCount={cpCount} mode={mode} onOpenChange={(v) => !v && setAdding(undefined)} onJob={setJob} />
    </Card>
  );
}

// --- OS updates ----------------------------------------------------------------

function OSCard({ id, jobs, members }: { id: string; jobs?: Job[]; members: Server[] }) {
  const qc = useQueryClient();
  const [policy, setPolicy] = useState<RebootPolicy>("if-required");
  const [job, setJob] = useState<Job>();
  const [open, setOpen] = useState<string>();
  const onJob = (j: Job) => {
    setJob(j);
    qc.invalidateQueries({ queryKey: ["jobs", "cluster", id] });
  };
  const check = useMutation({ mutationFn: () => api.osCheck(id), onSuccess: onJob });
  const update = useMutation({ mutationFn: () => api.osUpdate(id, [], policy), onSuccess: onJob });
  const reboot = useMutation({ mutationFn: () => api.rollingReboot(id, []), onSuccess: onJob });
  const last = latest(jobs, "cluster.os-check");
  const reports = ((last?.result as { nodes?: OSNodeReport[] } | undefined)?.nodes ?? []).map((r) => ({ ...r, packages: r.packages ?? [], held: r.held ?? [] }));
  const totalSec = reports.reduce((a, r) => a + r.security, 0);
  const totalPkgs = reports.reduce((a, r) => a + r.packages.length, 0);

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <PackageCheck className="text-primary size-4" /> Sistema operacional (Debian)
        </CardTitle>
        <CardDescription>
          apt-get upgrade nó a nó preservando os holds de kubelet/kubeadm/kubectl, com destaque para pacotes de segurança e reboot quando
          /var/run/reboot-required existir.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex flex-wrap items-center gap-2">
          <Button requires="operator" variant="outline" onClick={() => check.mutate()} disabled={check.isPending || (last && !isFinal(last.status))}>
            {check.isPending || (last && !isFinal(last.status)) ? <Loader2 className="animate-spin" /> : <Search />} Verificar atualizações
          </Button>
          <NativeSelect className="w-60" value={policy} onChange={(e) => setPolicy(e.target.value as RebootPolicy)} aria-label="Política de reboot">
            <option value="if-required">Reiniciar se necessário</option>
            <option value="never">Não reiniciar</option>
            <option value="always">Sempre reiniciar</option>
          </NativeSelect>
          <ConfirmButton
            variant="default"
            size="default"
            disabled={update.isPending}
            title="Atualizar o sistema de todos os nós?"
            description={
              <p>
                Um nó por vez: apt-get upgrade (holds preservados){policy !== "never" && " → se for reiniciar: cordon, drain, reboot, aguardar SSH e Ready, uncordon"}. Falhas pausam o job.
              </p>
            }
            confirmLabel="Atualizar"
            onConfirm={() => update.mutate()}
          >
            <PackageCheck /> Aplicar atualizações
          </ConfirmButton>
          <ConfirmButton
            variant="outline"
            size="default"
            disabled={reboot.isPending}
            title="Rolling reboot do cluster?"
            description={<p>Cada nó, em sequência: cordon → drain → reboot → aguarda SSH (novo boot) e Ready → uncordon.</p>}
            confirmLabel="Reiniciar em sequência"
            onConfirm={() => reboot.mutate()}
          >
            <Power /> Rolling reboot
          </ConfirmButton>
        </div>
        <ErrorText error={check.error ?? update.error ?? reboot.error} />
        {job && <JobLink job={job} />}
        {last && isFinal(last.status) && reports.length > 0 && (
          <>
            <div className="text-muted-foreground text-xs">
              Verificado {formatDate(last.finishedAt)} · {totalPkgs} pacote(s) atualizável(is), {totalSec} de segurança
            </div>
            <div className="divide-y rounded-md border">
              {reports.map((r) => {
                const srv = members.find((m) => m.id === r.serverId);
                return (
                  <div key={r.serverId}>
                    <button className="hover:bg-accent/50 flex w-full flex-wrap items-center gap-3 px-3 py-2 text-left text-sm" onClick={() => setOpen(open === r.serverId ? undefined : r.serverId)}>
                      <span className="font-medium">{r.name}</span>
                      <span className="text-muted-foreground text-xs">{srv?.host}</span>
                      {r.error ? (
                        <Badge variant="destructive">erro</Badge>
                      ) : (
                        <>
                          <Badge variant={r.packages.length ? "info" : "success"}>{r.packages.length ? `${r.packages.length} atualizável(is)` : "em dia"}</Badge>
                          {r.security > 0 && (
                            <Badge variant="destructive">
                              <ShieldAlert /> {r.security} segurança
                            </Badge>
                          )}
                          {r.rebootRequired && <Badge variant="warning">reboot pendente</Badge>}
                        </>
                      )}
                      <span className="text-muted-foreground ml-auto font-mono text-xs">{r.kernel}</span>
                    </button>
                    {open === r.serverId && (
                      <div className="bg-muted/30 px-3 py-2">
                        {r.error && <p className="text-destructive text-xs">{r.error}</p>}
                        {r.packages.length > 0 && (
                          <Table>
                            <TableHeader>
                              <TableRow>
                                <TableHead>Pacote</TableHead>
                                <TableHead>De</TableHead>
                                <TableHead>Para</TableHead>
                                <TableHead>Origem</TableHead>
                              </TableRow>
                            </TableHeader>
                            <TableBody>
                              {r.packages.map((p) => (
                                <TableRow key={p.name}>
                                  <TableCell className="font-mono text-xs">
                                    {p.name} {p.held && <Badge variant="secondary">hold (não atualiza)</Badge>}
                                  </TableCell>
                                  <TableCell className="font-mono text-xs">{p.from}</TableCell>
                                  <TableCell className="font-mono text-xs">{p.to}</TableCell>
                                  <TableCell className="text-xs">{p.security ? <Badge variant="destructive">{p.suite}</Badge> : p.suite}</TableCell>
                                </TableRow>
                              ))}
                            </TableBody>
                          </Table>
                        )}
                        {r.held.length > 0 && <p className="text-muted-foreground mt-2 text-xs">Em hold: {r.held.join(", ")}</p>}
                      </div>
                    )}
                  </div>
                );
              })}
            </div>
          </>
        )}
        {!last && <p className="text-muted-foreground text-sm">Ainda não verificado. Clique em “Verificar atualizações”.</p>}
      </CardContent>
    </Card>
  );
}

// --- Certificates --------------------------------------------------------------

function daysLeft(iso: string) {
  return Math.floor((new Date(iso).getTime() - Date.now()) / 86_400_000);
}

function CertsCard({ id, jobs }: { id: string; jobs?: Job[] }) {
  const qc = useQueryClient();
  const [job, setJob] = useState<Job>();
  const run = useMutation({
    mutationFn: (a: "check" | "renew") => api.certs(id, a),
    onSuccess: (j) => {
      setJob(j);
      qc.invalidateQueries({ queryKey: ["jobs", "cluster", id] });
    },
  });
  const last = latest(jobs, "cluster.certs", (j) => !!j.result);
  const running = latest(jobs, "cluster.certs", (j) => !isFinal(j.status));
  const res = last?.result as CertsResult | undefined;
  const certs = (res?.nodes ?? []).flatMap((n) => (n.certs ?? []).filter((c) => !c.authority).map((c) => ({ ...c, node: n.name }))) ?? [];
  const soonest = certs.reduce<number | undefined>((min, c) => (min === undefined ? daysLeft(c.expires) : Math.min(min, daysLeft(c.expires))), undefined);

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <KeyRound className="text-primary size-4" /> Certificados do control plane
        </CardTitle>
        <CardDescription>
          kubeadm certs check-expiration em cada control plane. A renovação roda “kubeadm certs renew all” um CP por vez, reinicia os static pods e atualiza o
          admin.conf cifrado da plataforma. Upgrades de versão também renovam.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex flex-wrap items-center gap-2">
          <Button requires="operator" variant="outline" onClick={() => run.mutate("check")} disabled={run.isPending || !!running}>
            {running ? <Loader2 className="animate-spin" /> : <Search />} Verificar validade
          </Button>
          <ConfirmButton
            variant="outline"
            size="default"
            disabled={run.isPending || !!running}
            title="Renovar todos os certificados?"
            description={<p>Em cada control plane, um por vez: kubeadm certs renew all → reinício de etcd, kube-apiserver, controller-manager, scheduler (e kube-vip). A API fica indisponível por alguns segundos em clusters com um único CP.</p>}
            confirmLabel="Renovar"
            onConfirm={() => run.mutate("renew")}
          >
            <RotateCw /> Renovar
          </ConfirmButton>
          {soonest !== undefined && (
            <Badge variant={soonest < 30 ? "destructive" : soonest < 90 ? "warning" : "success"} className="ml-auto">
              Próximo vencimento em {soonest} dia(s)
            </Badge>
          )}
        </div>
        <ErrorText error={run.error} />
        {job && <JobLink job={job} />}
        {res && (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Certificado</TableHead>
                <TableHead>Control plane</TableHead>
                <TableHead>Vence em</TableHead>
                <TableHead>Restante</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {certs.map((c) => {
                const d = daysLeft(c.expires);
                return (
                  <TableRow key={c.node + c.name}>
                    <TableCell className="font-mono text-xs">{c.name}</TableCell>
                    <TableCell className="text-sm">{c.node}</TableCell>
                    <TableCell className="text-sm">{formatDate(c.expires)}</TableCell>
                    <TableCell>
                      <Badge variant={d < 30 ? "destructive" : d < 90 ? "warning" : "secondary"}>{c.residual}</Badge>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        )}
        {!res && <p className="text-muted-foreground text-sm">Ainda não verificado.</p>}
      </CardContent>
    </Card>
  );
}

// --- History -------------------------------------------------------------------

function HistoryCard({ jobs }: { jobs?: Job[] }) {
  const list = (jobs ?? []).filter((j) => lifecycleKinds.has(j.kind)).slice(0, 15);
  return (
    <Card className="py-0">
      <CardHeader className="pt-6">
        <CardTitle>Histórico de manutenção</CardTitle>
      </CardHeader>
      <CardContent className="px-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Operação</TableHead>
              <TableHead>Estado</TableHead>
              <TableHead>Início</TableHead>
              <TableHead>Duração</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {list.map((j) => (
              <TableRow key={j.id}>
                <TableCell>
                  <Link to={`/jobs/${j.id}`} className="font-medium hover:underline">
                    {jobKindLabel[j.kind] ?? j.kind}
                  </Link>
                  {j.error && <div className="text-destructive max-w-md truncate text-xs" title={j.error}>{j.error}</div>}
                </TableCell>
                <TableCell>
                  <JobStatusBadge status={j.status} />
                </TableCell>
                <TableCell className="text-sm">{formatDate(j.startedAt ?? j.createdAt)}</TableCell>
                <TableCell className="text-sm">{duration(j.startedAt, j.finishedAt)}</TableCell>
              </TableRow>
            ))}
            {list.length === 0 && (
              <TableRow>
                <TableCell colSpan={4} className="text-muted-foreground py-6 text-center text-sm">
                  Nenhuma operação ainda.
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
  );
}

/** Go encodes empty slices as null: normalise before rendering. */
function safePlan(p: UpgradePlan): UpgradePlan {
  return { ...p, nodes: p.nodes ?? [], checks: p.checks ?? [], targets: (p.targets ?? []).map((t) => ({ ...t, hops: t.hops ?? [] })) };
}

/**
 * Shown while the cluster API does not answer (e.g. kube-apiserver restarting
 * during an upgrade): the page keeps the last data it had instead of vanishing.
 */
function ApiUnavailable({ error, running, stale }: { error: unknown; running?: Job; stale: boolean }) {
  return (
    <div className="border-warning/40 bg-warning/10 rounded-lg border px-4 py-3 text-sm">
      <div className="font-medium">A API do Kubernetes não respondeu agora; tentando de novo automaticamente.</div>
      <div className="text-muted-foreground mt-0.5 text-xs">
        {running
          ? `É esperado por alguns instantes enquanto "${jobKindLabel[running.kind] ?? running.kind}" reinicia componentes do cluster. `
          : ""}
        {stale ? "Os dados abaixo são da última leitura. " : ""}
        <span className="opacity-80">Detalhe: {error instanceof Error ? error.message : String(error)}</span>
      </div>
      {running && (
        <div className="mt-2">
          <JobLink job={running} />
        </div>
      )}
    </div>
  );
}

export function ClusterLifecyclePage() {
  const { id = "" } = useParams();
  const jobs = useClusterJobs(id);
  const running = (jobs.data ?? []).find((j) => !isFinal(j.status) && lifecycleKinds.has(j.kind));
  const plan = useQuery({ queryKey: ["upgradePlan", id], queryFn: () => api.upgradePlan(id), refetchInterval: running ? 5_000 : 30_000, retry: 1 });
  const cluster = useQuery({ queryKey: ["cluster", id], queryFn: () => api.cluster(id) });
  const members = useMemo(() => cluster.data?.nodes ?? [], [cluster.data]);
  const p = plan.data ? safePlan(plan.data) : undefined;
  const notReady = plan.error instanceof ApiError && plan.error.code === "not_ready";

  return (
    <>
      <ClusterHeader id={id} />
      {plan.isLoading ? (
        <Loader2 className="text-muted-foreground animate-spin" />
      ) : notReady ? (
        <KubeError error={plan.error} />
      ) : (
        <div className="space-y-6">
          {plan.error && <ApiUnavailable error={plan.error} running={running} stale={!!p} />}
          {p && <UpgradeCard id={id} plan={p} jobs={jobs.data ?? []} />}
          {p && <NodesCard id={id} plan={p} members={members} cluster={cluster.data} />}
          <div className="grid gap-6 2xl:grid-cols-2">
            <OSCard id={id} jobs={jobs.data} members={members} />
            <CertsCard id={id} jobs={jobs.data} />
          </div>
          <HistoryCard jobs={jobs.data} />
        </div>
      )}
    </>
  );
}
