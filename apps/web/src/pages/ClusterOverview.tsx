import { Link, useParams } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, CheckCircle2, Cpu, Loader2, MemoryStick, Share2, XCircle } from "lucide-react";
import { api, type ClusterOverview, type ResourceTotals } from "@/lib/api";
import { cn, formatBytes } from "@/lib/utils";
import { ClusterHeader, KubeError } from "@/components/cluster/ClusterHeader";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { age, NodeBadge } from "@/components/cluster/k8s";

const pct = (a: number | null | undefined, b: number) => (a == null || !b ? null : Math.min(100, (a / b) * 100));
const cpuText = (m: number) => (m >= 1000 ? `${(m / 1000).toFixed(m % 1000 ? 1 : 0)} vCPU` : `${m}m`);

/** Thin meter: requested (light) behind used (solid), both over allocatable. */
function Meter({ used, requested, total, className }: { used: number | null; requested: number; total: number; className?: string }) {
  const u = pct(used, total);
  const r = pct(requested, total) ?? 0;
  const tone = (u ?? r) >= 90 ? "bg-destructive" : (u ?? r) >= 75 ? "bg-warning" : "bg-primary";
  return (
    <div className={cn("bg-muted relative h-2 overflow-hidden rounded-full", className)}>
      <div className="bg-primary/30 absolute inset-y-0 left-0 rounded-full" style={{ width: `${r}%` }} />
      {u != null && <div className={cn("absolute inset-y-0 left-0 rounded-full", tone)} style={{ width: `${u}%` }} />}
    </div>
  );
}

function ResourceCard({ title, icon, totals, fmt, metrics }: { title: string; icon: React.ReactNode; totals: ResourceTotals; fmt: (n: number) => string; metrics: boolean }) {
  const used = pct(totals.used, totals.allocatable);
  const req = pct(totals.requested, totals.allocatable) ?? 0;
  return (
    <Card className="gap-3">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-sm font-medium">
          {icon} {title}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="flex items-baseline gap-2">
          <span className="text-3xl font-semibold tabular-nums">{used != null ? `${Math.round(used)}%` : `${Math.round(req)}%`}</span>
          <span className="text-muted-foreground text-sm">{used != null ? "em uso agora" : "reservado (requests)"}</span>
        </div>
        <Meter used={totals.used} requested={totals.requested} total={totals.allocatable} className="h-2.5" />
        <div className="text-muted-foreground grid grid-cols-3 gap-2 text-xs">
          <div>
            <div className="flex items-center gap-1">
              <span className="bg-primary size-2 rounded-full" /> Uso
            </div>
            <div className="text-foreground font-medium">{totals.used != null ? fmt(totals.used) : "—"}</div>
          </div>
          <div>
            <div className="flex items-center gap-1">
              <span className="bg-primary/30 size-2 rounded-full" /> Requests
            </div>
            <div className="text-foreground font-medium">{fmt(totals.requested)}</div>
          </div>
          <div>
            <div>Alocável</div>
            <div className="text-foreground font-medium">{fmt(totals.allocatable)}</div>
          </div>
        </div>
        {!metrics && <p className="text-muted-foreground text-xs">Instale o metrics-server para ver o consumo real.</p>}
      </CardContent>
    </Card>
  );
}

function Tile({ label, value, sub, tone }: { label: string; value: React.ReactNode; sub?: React.ReactNode; tone?: "ok" | "warn" | "bad" }) {
  const Icon = tone === "bad" ? XCircle : tone === "warn" ? AlertTriangle : CheckCircle2;
  return (
    <Card className="gap-1 py-4">
      <CardContent className="space-y-1">
        <div className="text-muted-foreground flex items-center justify-between text-xs">
          {label}
          {tone && <Icon className={cn("size-4", tone === "bad" ? "text-destructive" : tone === "warn" ? "text-warning" : "text-success")} />}
        </div>
        <div className="text-3xl font-semibold tabular-nums">{value}</div>
        {sub && <div className="text-muted-foreground text-xs">{sub}</div>}
      </CardContent>
    </Card>
  );
}

export function ClusterOverviewPage() {
  const { id = "" } = useParams();
  const ov = useQuery({ queryKey: ["overview", id], queryFn: () => api.overview(id), refetchInterval: 10_000 });
  return (
    <>
      <ClusterHeader
        id={id}
        right={
          <>
            {ov.data && <span className="text-muted-foreground text-xs">Atualizado {new Date(ov.data.collectedAt).toLocaleTimeString("pt-BR")}</span>}
            <Button asChild>
              <Link to={`/clusters/${id}/topology`}>
                <Share2 /> Topologia ao vivo
              </Link>
            </Button>
          </>
        }
      />
      {ov.isLoading ? <Loader2 className="text-muted-foreground animate-spin" /> : ov.error ? <KubeError error={ov.error} /> : ov.data && <Overview o={ov.data} />}
    </>
  );
}

function Overview({ o }: { o: ClusterOverview }) {
  const notReady = o.nodes.length - o.nodesReady;
  return (
    <div className="space-y-4">
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Tile label="Nós prontos" value={`${o.nodesReady}/${o.nodes.length}`} sub={`API ${o.version} · ${o.platform}`} tone={notReady ? "bad" : "ok"} />
        <Tile
          label="Pods rodando"
          value={o.pods.running}
          sub={
            <>
              {o.pods.total} no total · {o.pods.pending} pendentes
              {o.pods.problem > 0 && <span className="text-destructive"> · {o.pods.problem} com problema</span>}
            </>
          }
          tone={o.pods.problem ? "bad" : o.pods.pending ? "warn" : "ok"}
        />
        <Tile
          label="Deployments disponíveis"
          value={`${o.deploymentsReady}/${o.deployments}`}
          tone={o.deploymentsReady < o.deployments ? "warn" : "ok"}
          sub="réplicas prontas ≥ desejadas"
        />
        <Tile label="Namespaces · Services" value={`${o.namespaces} · ${o.services}`} sub="objetos de rede e isolamento" />
      </div>

      <div className="grid gap-3 md:grid-cols-2">
        <ResourceCard title="CPU do cluster" icon={<Cpu className="size-4" />} totals={o.cpu} fmt={cpuText} metrics={o.metricsAvailable} />
        <ResourceCard title="Memória do cluster" icon={<MemoryStick className="size-4" />} totals={o.memory} fmt={(n) => formatBytes(n)} metrics={o.metricsAvailable} />
      </div>

      <Card className="py-0">
        <CardHeader className="pt-5">
          <CardTitle>Nós</CardTitle>
        </CardHeader>
        <CardContent className="px-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Nó</TableHead>
                <TableHead>Estado</TableHead>
                <TableHead>Kubelet</TableHead>
                <TableHead>Pods</TableHead>
                <TableHead className="w-48">CPU</TableHead>
                <TableHead className="w-48">Memória</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {o.nodes.map((n) => {
                const cpuUsed = n.cpuUsedMillis ?? n.cpuRequestedMillis;
                const memUsed = n.memoryUsedBytes ?? n.memoryRequestedBytes;
                return (
                  <TableRow key={n.uid}>
                    <TableCell>
                      <div className="flex items-center gap-2 font-medium">
                        {n.name}
                        {n.roles?.includes("control-plane") && <Badge variant="info">Control Plane</Badge>}
                      </div>
                      <div className="text-muted-foreground font-mono text-xs">{n.internalIP}</div>
                    </TableCell>
                    <TableCell>
                      <NodeBadge n={n} />
                      {!!n.conditions?.length && <div className="text-warning mt-1 text-xs">{n.conditions.join(", ")}</div>}
                    </TableCell>
                    <TableCell className="text-sm">{n.kubeletVersion}</TableCell>
                    <TableCell className="tabular-nums">
                      {n.pods}
                      <span className="text-muted-foreground">/{n.podsCapacity}</span>
                    </TableCell>
                    <TableCell title={`Uso ${n.cpuUsedMillis != null ? cpuText(n.cpuUsedMillis) : "—"} · requests ${cpuText(n.cpuRequestedMillis)} · alocável ${cpuText(n.cpuMillis)}`}>
                      <Meter used={n.cpuUsedMillis} requested={n.cpuRequestedMillis} total={n.cpuMillis} />
                      <div className="text-muted-foreground mt-1 text-xs tabular-nums">
                        {cpuText(cpuUsed)} de {cpuText(n.cpuMillis)}
                      </div>
                    </TableCell>
                    <TableCell
                      title={`Uso ${n.memoryUsedBytes != null ? formatBytes(n.memoryUsedBytes) : "—"} · requests ${formatBytes(n.memoryRequestedBytes)} · alocável ${formatBytes(n.memoryBytes)}`}
                    >
                      <Meter used={n.memoryUsedBytes} requested={n.memoryRequestedBytes} total={n.memoryBytes} />
                      <div className="text-muted-foreground mt-1 text-xs tabular-nums">
                        {formatBytes(memUsed)} de {formatBytes(n.memoryBytes)}
                      </div>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <div className="grid gap-3 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>Control plane</CardTitle>
          </CardHeader>
          <CardContent className="grid gap-2 sm:grid-cols-2">
            {o.controlPlane.length === 0 && <p className="text-muted-foreground text-sm">Nenhum componente estático encontrado.</p>}
            {o.controlPlane.map((c) => (
              <div key={`${c.name}-${c.node}`} className="flex items-center gap-2 rounded-md border px-3 py-2 text-sm">
                {c.ready ? <CheckCircle2 className="text-success size-4" /> : <XCircle className="text-destructive size-4" />}
                <div className="min-w-0">
                  <div className="truncate font-medium">{c.name}</div>
                  <div className="text-muted-foreground text-xs">
                    {c.node} · {c.ready ? "saudável" : "com falha"}
                    {c.restarts > 0 && ` · ${c.restarts} reinícios`}
                  </div>
                </div>
              </div>
            ))}
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle>Add-ons do sistema</CardTitle>
          </CardHeader>
          <CardContent className="space-y-1.5">
            {o.system.map((w) => {
              const ok = w.ready >= w.desired && w.desired > 0;
              return (
                <div key={`${w.namespace}/${w.name}`} className="flex items-center gap-2 text-sm">
                  {ok ? <CheckCircle2 className="text-success size-4" /> : <AlertTriangle className="text-warning size-4" />}
                  <span className="font-medium">{w.name}</span>
                  <span className="text-muted-foreground text-xs">
                    {w.kind} · {w.namespace}
                  </span>
                  <span className="ml-auto tabular-nums">
                    {w.ready}/{w.desired}
                  </span>
                </div>
              );
            })}
          </CardContent>
        </Card>
      </div>

      <Card className="py-0">
        <CardHeader className="pt-5">
          <CardTitle>Alertas recentes</CardTitle>
        </CardHeader>
        <CardContent className="px-0">
          {o.events.length === 0 ? (
            <p className="text-muted-foreground flex items-center gap-2 px-6 pb-5 text-sm">
              <CheckCircle2 className="text-success size-4" /> Nenhum evento de alerta. O cluster está tranquilo.
            </p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Quando</TableHead>
                  <TableHead>Objeto</TableHead>
                  <TableHead>Motivo</TableHead>
                  <TableHead>Mensagem</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {o.events.map((e, i) => (
                  <TableRow key={i}>
                    <TableCell className="text-muted-foreground text-xs whitespace-nowrap">
                      há {age(e.last)}
                      {e.count > 1 && ` · ${e.count}×`}
                    </TableCell>
                    <TableCell className="text-sm">
                      <div className="font-medium">{e.name}</div>
                      <div className="text-muted-foreground text-xs">
                        {e.kind} · {e.namespace}
                      </div>
                    </TableCell>
                    <TableCell>
                      <Badge variant="warning">{e.reason}</Badge>
                    </TableCell>
                    <TableCell className="max-w-md text-xs break-words whitespace-normal">{e.message}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
