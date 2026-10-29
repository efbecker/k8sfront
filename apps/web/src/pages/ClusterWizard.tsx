import { useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, ChevronLeft, ChevronRight, Loader2, Network, Plus, Rocket, Server as ServerIcon, Stethoscope } from "lucide-react";
import { api, isFinal, type CNI, type ClusterInput, type Topology } from "@/lib/api";
import { cn } from "@/lib/utils";
import { useJobStream } from "@/hooks/useJobStream";
import { PageHeader } from "@/components/layout/AppShell";
import { CheckBadge, JobStatusBadge, sudoLabel } from "@/components/status";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, NativeSelect } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ServerFormDialog } from "./ServerForm";

type Role = "control-plane" | "worker";
const steps = ["Topologia e parâmetros", "Servidores Debian 13+", "Revisão"] as const;

function Choice({ active, onClick, title, desc, icon }: { active: boolean; onClick: () => void; title: string; desc: string; icon?: React.ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "flex flex-1 items-start gap-3 rounded-lg border p-4 text-left transition-colors",
        active ? "border-primary bg-primary/5 ring-primary/30 ring-2" : "hover:bg-accent",
      )}
    >
      {icon}
      <div>
        <div className="flex items-center gap-2 font-medium">
          {title} {active && <Check className="text-primary size-4" />}
        </div>
        <div className="text-muted-foreground mt-1 text-xs">{desc}</div>
      </div>
    </button>
  );
}

function Toggle({ checked, onChange, label, hint }: { checked: boolean; onChange: (v: boolean) => void; label: string; hint?: string }) {
  return (
    <label className="flex cursor-pointer items-start gap-3">
      <input type="checkbox" className="accent-primary mt-0.5 size-4" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      <span>
        <span className="text-sm font-medium">{label}</span>
        {hint && <span className="text-muted-foreground block text-xs">{hint}</span>}
      </span>
    </label>
  );
}

const cidrRe = /^(\d{1,3}\.){3}\d{1,3}\/\d{1,2}$/;
const ipRe = /^(\d{1,3}\.){3}\d{1,3}$/;

export function ClusterWizardPage() {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [step, setStep] = useState(0);
  const [name, setName] = useState("producao");
  const [version, setVersion] = useState("");
  const [cni, setCni] = useState<CNI>("cilium");
  const [podCidr, setPodCidr] = useState("10.244.0.0/16");
  const [serviceCidr, setServiceCidr] = useState("10.96.0.0/12");
  const [topology, setTopology] = useState<Topology>("multi-node");
  const [untaint, setUntaint] = useState(false);
  const [metrics, setMetrics] = useState(true);
  const [vipOn, setVipOn] = useState(false);
  const [vip, setVip] = useState("");
  const [vipIface, setVipIface] = useState("");
  const [roles, setRoles] = useState<Record<string, Role | "">>({});
  const [adding, setAdding] = useState(false);
  const [testJob, setTestJob] = useState<string>();

  const versions = useQuery({ queryKey: ["k8s-versions"], queryFn: api.kubernetesVersions, staleTime: 3600_000, retry: 1 });
  useEffect(() => {
    if (!version && versions.data?.[0]) setVersion(versions.data[0].version);
  }, [versions.data, version]);
  const servers = useQuery({ queryKey: ["servers"], queryFn: api.servers });
  const free = (servers.data ?? []).filter((s) => !s.clusterId);
  const selected = free.filter((s) => roles[s.id]);
  const cps = selected.filter((s) => roles[s.id] === "control-plane");
  const workers = selected.filter((s) => roles[s.id] === "worker");

  const { job: liveTest } = useJobStream(testJob, () => {});
  const testing = !!testJob && (!liveTest || !isFinal(liveTest.status));
  const test = useMutation({ mutationFn: () => api.preflightMany(selected.map((s) => s.id)), onSuccess: (j) => setTestJob(j.id) });
  useEffect(() => {
    if (liveTest && isFinal(liveTest.status)) qc.invalidateQueries({ queryKey: ["servers"] });
  }, [liveTest, qc]);

  const setRole = (id: string, role: Role | "") => {
    setRoles((r) => {
      const next = { ...r, [id]: role };
      if (topology === "single-node" && role) for (const k of Object.keys(next)) if (k !== id) next[k] = "";
      return next;
    });
  };

  const errors1: string[] = [];
  if (!/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(name)) errors1.push("Nome deve ser um rótulo DNS (minúsculas, números e '-').");
  if (!version) errors1.push("Selecione a versão do Kubernetes.");
  if (!cidrRe.test(podCidr) || !cidrRe.test(serviceCidr)) errors1.push("CIDRs inválidos.");
  if (vipOn && !ipRe.test(vip)) errors1.push("Informe o endereço IPv4 do VIP.");

  const errors2: string[] = [];
  if (cps.length === 0) errors2.push("Selecione ao menos um Control Plane.");
  if (topology === "single-node" && (cps.length !== 1 || workers.length > 0)) errors2.push("Single-node usa exatamente um servidor como Control Plane.");
  if (cps.length > 1 && cps.length % 2 === 0) errors2.push("Use um número ímpar de Control Planes (quórum do etcd).");
  if (cps.length > 1 && !vipOn) errors2.push("Mais de um Control Plane exige VIP (kube-vip). Volte ao passo 1 para ativar.");
  const untested = selected.filter((s) => !s.lastCheckStatus);
  const failing = selected.filter((s) => s.lastCheckStatus === "fail");
  if (untested.length) errors2.push(`Teste os pré-requisitos de: ${untested.map((s) => s.name).join(", ")}.`);
  if (failing.length) errors2.push(`Corrija os servidores reprovados: ${failing.map((s) => s.name).join(", ")}.`);

  const create = useMutation({
    mutationFn: () => {
      const input: ClusterInput = {
        name,
        kubernetesVersion: version,
        podCidr,
        serviceCidr,
        cni,
        topology,
        options: {
          untaintControlPlane: topology === "single-node" || untaint,
          metricsServer: metrics,
          kubeVip: vipOn ? { address: vip, interface: vipIface || undefined } : undefined,
        },
        nodes: [...cps, ...workers].map((s) => ({ serverId: s.id, role: roles[s.id] as Role })),
        start: true,
      };
      return api.createCluster(input);
    },
    onSuccess: (c) => {
      qc.invalidateQueries({ queryKey: ["clusters"] });
      qc.invalidateQueries({ queryKey: ["servers"] });
      navigate(`/clusters/${c.id}`);
    },
  });

  const canNext = step === 0 ? errors1.length === 0 : step === 1 ? errors2.length === 0 : true;
  const summary = useMemo(
    () => [
      ["Cluster", name],
      ["Kubernetes", version],
      ["CNI", cni === "cilium" ? "Cilium" : "Calico"],
      ["Topologia", topology === "single-node" ? "Single-node" : "Multi-node"],
      ["Pod CIDR", podCidr],
      ["Service CIDR", serviceCidr],
      ["Endpoint", vipOn ? `${vip}:6443 (kube-vip${vipIface ? ` em ${vipIface}` : ""})` : `${cps[0]?.host ?? "—"}:6443`],
      ["Workloads no Control Plane", topology === "single-node" || untaint ? "permitidos" : "não"],
      ["metrics-server", metrics ? "sim" : "não"],
    ],
    [name, version, cni, topology, podCidr, serviceCidr, vipOn, vip, vipIface, cps, untaint, metrics],
  );

  return (
    <>
      <PageHeader title="Wizard de Deploy" description="Provisione um cluster Kubernetes com kubeadm em servidores Debian 13 (Trixie) ou superior." />
      <ol className="mb-6 flex flex-wrap gap-2">
        {steps.map((label, i) => (
          <li
            key={label}
            className={cn(
              "flex items-center gap-2 rounded-full border px-3 py-1 text-sm",
              i === step ? "border-primary bg-primary/10 text-primary" : i < step ? "text-foreground" : "text-muted-foreground",
            )}
          >
            <span className={cn("flex size-5 items-center justify-center rounded-full text-xs", i <= step ? "bg-primary text-primary-foreground" : "bg-muted")}>
              {i < step ? <Check className="size-3" /> : i + 1}
            </span>
            {label}
          </li>
        ))}
      </ol>

      {step === 0 && (
        <div className="grid gap-6 lg:grid-cols-2">
          <Card>
            <CardHeader>
              <CardTitle>Cluster</CardTitle>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="space-y-1.5">
                <Label>Nome do cluster</Label>
                <Input value={name} onChange={(e) => setName(e.target.value.toLowerCase())} />
              </div>
              <div className="space-y-1.5">
                <Label>Versão do Kubernetes</Label>
                {versions.isLoading ? (
                  <div className="text-muted-foreground flex items-center gap-2 text-sm">
                    <Loader2 className="size-4 animate-spin" /> Consultando versões estáveis em dl.k8s.io…
                  </div>
                ) : versions.data?.length ? (
                  <NativeSelect value={version} onChange={(e) => setVersion(e.target.value)}>
                    {versions.data.map((v) => (
                      <option key={v.version} value={v.version}>
                        {v.version}
                        {v.latest ? " (mais recente)" : ""}
                      </option>
                    ))}
                  </NativeSelect>
                ) : (
                  <>
                    <Input value={version} onChange={(e) => setVersion(e.target.value)} placeholder="v1.37.2" />
                    <p className="text-warning text-xs">Não foi possível listar as versões ({versions.error?.message}). Informe manualmente.</p>
                  </>
                )}
                <p className="text-muted-foreground text-xs">Pacotes do repositório oficial pkgs.k8s.io, mantidos em apt-mark hold.</p>
              </div>
              <div className="grid grid-cols-2 gap-4">
                <div className="space-y-1.5">
                  <Label>Pod CIDR</Label>
                  <Input value={podCidr} onChange={(e) => setPodCidr(e.target.value)} className="font-mono" />
                </div>
                <div className="space-y-1.5">
                  <Label>Service CIDR</Label>
                  <Input value={serviceCidr} onChange={(e) => setServiceCidr(e.target.value)} className="font-mono" />
                </div>
              </div>
              <div className="space-y-1.5">
                <Label>Plugin de rede (CNI)</Label>
                <div className="flex gap-3">
                  <Choice active={cni === "cilium"} onClick={() => setCni("cilium")} title="Cilium" desc="eBPF, instalado via cilium-cli (v1.20)." icon={<Network className="text-primary mt-0.5 size-5" />} />
                  <Choice active={cni === "calico"} onClick={() => setCni("calico")} title="Calico" desc="Tigera operator, VXLAN cross-subnet (v3.32)." icon={<Network className="text-primary mt-0.5 size-5" />} />
                </div>
              </div>
            </CardContent>
          </Card>
          <Card>
            <CardHeader>
              <CardTitle>Topologia</CardTitle>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="flex gap-3">
                <Choice
                  active={topology === "single-node"}
                  onClick={() => {
                    setTopology("single-node");
                    setVipOn(false);
                    setRoles({});
                  }}
                  title="Single-node"
                  desc="Um servidor como Control Plane; o taint é removido para rodar workloads."
                />
                <Choice active={topology === "multi-node"} onClick={() => setTopology("multi-node")} title="Multi-node" desc="1 ou mais Control Planes (HA com kube-vip) e Workers." />
              </div>
              {topology === "multi-node" && (
                <>
                  <Toggle checked={vipOn} onChange={setVipOn} label="VIP do Control Plane com kube-vip" hint="Obrigatório para mais de um Control Plane. Modo ARP, mesmo segmento L2 dos nós." />
                  {vipOn && (
                    <div className="grid grid-cols-2 gap-4 pl-7">
                      <div className="space-y-1.5">
                        <Label>Endereço do VIP</Label>
                        <Input value={vip} onChange={(e) => setVip(e.target.value)} placeholder="192.168.10.50" className="font-mono" />
                      </div>
                      <div className="space-y-1.5">
                        <Label>Interface (opcional)</Label>
                        <Input value={vipIface} onChange={(e) => setVipIface(e.target.value)} placeholder="detectar (rota padrão)" className="font-mono" />
                      </div>
                    </div>
                  )}
                  <Toggle checked={untaint} onChange={setUntaint} label="Permitir workloads nos Control Planes" hint="Remove o taint node-role.kubernetes.io/control-plane." />
                </>
              )}
              <Toggle checked={metrics} onChange={setMetrics} label="Instalar metrics-server" hint="Habilita kubectl top e métricas de CPU/RAM na Dashboard." />
            </CardContent>
          </Card>
        </div>
      )}

      {step === 1 && (
        <Card>
          <CardHeader>
            <CardTitle>Servidores</CardTitle>
            <CardDescription>
              Escolha o papel de cada servidor livre. O primeiro Control Plane executa o <code>kubeadm init</code>.
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="flex flex-wrap gap-2">
              <Button variant="outline" onClick={() => setAdding(true)}>
                <Plus /> Adicionar servidor
              </Button>
              <Button variant="outline" disabled={selected.length === 0 || testing || test.isPending} onClick={() => test.mutate()}>
                {testing || test.isPending ? <Loader2 className="animate-spin" /> : <Stethoscope />}
                Testar Conexão & Pré-requisitos ({selected.length})
              </Button>
              {liveTest && (
                <span className="flex items-center gap-2 text-sm">
                  <JobStatusBadge status={liveTest.status} />
                  <a href={`/jobs/${liveTest.id}`} target="_blank" rel="noreferrer" className="text-muted-foreground hover:underline">
                    ver console
                  </a>
                </span>
              )}
            </div>
            {free.length === 0 ? (
              <p className="text-muted-foreground text-sm">Nenhum servidor livre. Adicione os servidores Debian 13+ do cluster.</p>
            ) : (
              <ul className="divide-y rounded-lg border">
                {free.map((s) => (
                  <li key={s.id} className="flex flex-wrap items-center gap-4 p-3">
                    <ServerIcon className="text-muted-foreground size-5" />
                    <div className="min-w-48 flex-1">
                      <div className="font-medium">{s.name}</div>
                      <div className="text-muted-foreground font-mono text-xs">
                        {s.sshUser}@{s.host}:{s.port} · {sudoLabel[s.sudoMode]}
                      </div>
                    </div>
                    <div className="text-muted-foreground w-48 text-xs">{s.osPrettyName || "SO não verificado"}</div>
                    <CheckBadge status={s.lastCheckStatus} />
                    <NativeSelect className="w-44" value={roles[s.id] ?? ""} onChange={(e) => setRole(s.id, e.target.value as Role | "")}>
                      <option value="">Não incluir</option>
                      <option value="control-plane">Control Plane</option>
                      {topology === "multi-node" && <option value="worker">Worker</option>}
                    </NativeSelect>
                  </li>
                ))}
              </ul>
            )}
            {cps[0] && (
              <p className="text-muted-foreground text-xs">
                <Badge variant="outline">kubeadm init</Badge> será executado em <strong>{cps[0].name}</strong>.
              </p>
            )}
          </CardContent>
        </Card>
      )}

      {step === 2 && (
        <div className="grid gap-6 lg:grid-cols-2">
          <Card>
            <CardHeader>
              <CardTitle>Parâmetros</CardTitle>
            </CardHeader>
            <CardContent>
              <dl className="divide-y">
                {summary.map(([k, v]) => (
                  <div key={k} className="flex justify-between gap-4 py-2 text-sm">
                    <dt className="text-muted-foreground">{k}</dt>
                    <dd className="text-right font-medium">{v}</dd>
                  </div>
                ))}
              </dl>
            </CardContent>
          </Card>
          <Card>
            <CardHeader>
              <CardTitle>Plano de execução</CardTitle>
              <CardDescription>Etapas idempotentes; uma falha pode ser retomada sem refazer o que já terminou.</CardDescription>
            </CardHeader>
            <CardContent>
              <ol className="list-decimal space-y-1.5 pl-5 text-sm">
                <li>Em paralelo em {selected.length} servidor(es): validar Debian 13+ e sudo, desativar swap, módulos/sysctl, containerd (SystemdCgroup), repositório pkgs.k8s.io, kubelet/kubeadm/kubectl {version} em hold.</li>
                <li>
                  <code>kubeadm init</code> em {cps[0]?.name}
                  {vipOn ? " com kube-vip" : ""}; admin.conf cifrado no backend.
                </li>
                <li>CNI {cni === "cilium" ? "Cilium" : "Calico"}.</li>
                {cps.length > 1 && <li>Join dos Control Planes {cps.slice(1).map((s) => s.name).join(", ")}, um por vez.</li>}
                {workers.length > 0 && <li>Join dos Workers {workers.map((s) => s.name).join(", ")}.</li>}
                {(topology === "single-node" || untaint) && <li>Remover taint do Control Plane.</li>}
                {metrics && <li>metrics-server.</li>}
                <li>Aguardar todos os nós em Ready.</li>
              </ol>
            </CardContent>
          </Card>
        </div>
      )}

      {(step === 0 ? errors1 : step === 1 ? errors2 : []).length > 0 && (
        <ul className="text-muted-foreground mt-4 list-disc space-y-1 pl-5 text-sm">
          {(step === 0 ? errors1 : errors2).map((e) => (
            <li key={e}>{e}</li>
          ))}
        </ul>
      )}
      {create.error && <p className="text-destructive mt-4 text-sm">{create.error.message}</p>}
      <div className="mt-6 flex justify-between">
        <Button variant="outline" disabled={step === 0} onClick={() => setStep((s) => s - 1)}>
          <ChevronLeft /> Voltar
        </Button>
        {step < 2 ? (
          <Button disabled={!canNext} onClick={() => setStep((s) => s + 1)}>
            Próximo <ChevronRight />
          </Button>
        ) : (
          <Button disabled={create.isPending} onClick={() => create.mutate()}>
            {create.isPending ? <Loader2 className="animate-spin" /> : <Rocket />}
            Criar cluster e iniciar deploy
          </Button>
        )}
      </div>
      {adding && (
        <ServerFormDialog
          open={adding}
          onOpenChange={setAdding}
          onSaved={(s) => setRole(s.id, s.role === "worker" && topology === "multi-node" ? "worker" : s.role === "control-plane" ? "control-plane" : "")}
        />
      )}
    </>
  );
}
