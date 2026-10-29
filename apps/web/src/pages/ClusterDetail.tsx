import { useMemo } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, Download, Loader2, Play, RotateCw, Trash2 } from "lucide-react";
import { api } from "@/lib/api";
import { PageHeader } from "@/components/layout/AppShell";
import { JobRun } from "@/components/JobRun";
import { CheckBadge, ClusterStatusBadge, JobStatusBadge, roleLabel } from "@/components/status";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

function Fact({ k, v }: { k: string; v: React.ReactNode }) {
  return (
    <div>
      <div className="text-muted-foreground text-xs">{k}</div>
      <div className="text-sm font-medium">{v}</div>
    </div>
  );
}

export function ClusterDetailPage() {
  const { id = "" } = useParams();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const cluster = useQuery({ queryKey: ["cluster", id], queryFn: () => api.cluster(id), refetchInterval: 4000 });
  const start = useMutation({
    mutationFn: () => api.bootstrapCluster(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["cluster", id] }),
  });
  const del = useMutation({
    mutationFn: () => api.deleteCluster(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["clusters"] });
      navigate("/clusters");
    },
  });
  const names = useMemo(() => new Map((cluster.data?.nodes ?? []).map((n) => [n.id, n.name])), [cluster.data?.nodes]);

  if (cluster.isLoading) return <Loader2 className="text-muted-foreground animate-spin" />;
  if (cluster.error || !cluster.data) return <p className="text-destructive">{cluster.error?.message ?? "Cluster não encontrado"}</p>;
  const c = cluster.data;
  const job = c.bootstrapJob;
  const running = job && (job.status === "running" || job.status === "pending");
  const resumable = job && (job.status === "failed" || job.status === "paused" || job.status === "cancelled");

  return (
    <>
      <Link to="/clusters" className="text-muted-foreground mb-3 inline-flex items-center gap-1 text-sm hover:underline">
        <ArrowLeft className="size-4" /> Clusters
      </Link>
      <PageHeader
        title={c.name}
        description={`Kubernetes ${c.kubernetesVersion} · ${c.cni === "cilium" ? "Cilium" : "Calico"} · ${c.topology === "single-node" ? "Single-node" : "Multi-node"}`}
        actions={
          <>
            <ClusterStatusBadge status={c.status} />
            {c.hasKubeconfig && (
              <Button variant="outline" asChild>
                <a href={api.kubeconfigUrl(c.id)} download>
                  <Download /> kubeconfig
                </a>
              </Button>
            )}
            {(c.status === "draft" || c.status === "failed") && !running && (
              <Button
                variant="outline"
                className="text-destructive"
                onClick={() =>
                  confirm("Remover este cluster do inventário? Os servidores voltam a ficar livres; nada é desfeito nos hosts.") && del.mutate()
                }
              >
                <Trash2 /> Remover
              </Button>
            )}
            {!running && c.status !== "ready" && (
              <Button disabled={start.isPending} onClick={() => start.mutate()}>
                {resumable ? <RotateCw /> : <Play />}
                {resumable ? "Retomar deploy" : "Iniciar deploy"}
              </Button>
            )}
          </>
        }
      />
      {(start.error || del.error) && <p className="text-destructive mb-4 text-sm">{(start.error ?? del.error)?.message}</p>}

      <Card className="mb-6">
        <CardContent className="grid grid-cols-2 gap-4 md:grid-cols-4 xl:grid-cols-7">
          <Fact k="Endpoint da API" v={<span className="font-mono text-xs">{c.apiServerUrl || c.options.kubeVip?.address || "—"}</span>} />
          <Fact k="Pod CIDR" v={<span className="font-mono text-xs">{c.podCidr}</span>} />
          <Fact k="Service CIDR" v={<span className="font-mono text-xs">{c.serviceCidr}</span>} />
          <Fact k="VIP (kube-vip)" v={c.options.kubeVip?.address ?? "não"} />
          <Fact k="metrics-server" v={c.options.metricsServer ? "sim" : "não"} />
          <Fact k="Workloads no CP" v={c.topology === "single-node" || c.options.untaintControlPlane ? "permitidos" : "não"} />
          <Fact k="Deploy" v={job ? <JobStatusBadge status={job.status} /> : "não iniciado"} />
        </CardContent>
      </Card>

      <Card className="mb-6 py-0">
        <CardHeader className="pt-6">
          <CardTitle>Servidores</CardTitle>
        </CardHeader>
        <CardContent className="px-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Servidor</TableHead>
                <TableHead>Papel</TableHead>
                <TableHead>Sistema</TableHead>
                <TableHead>Pré-requisitos</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {c.nodes.map((n, i) => (
                <TableRow key={n.id}>
                  <TableCell>
                    <Link to={`/servers/${n.id}`} className="font-medium hover:underline">
                      {n.name}
                    </Link>
                    {i === 0 && n.role === "control-plane" && (
                      <Badge variant="outline" className="ml-2">
                        kubeadm init
                      </Badge>
                    )}
                    <div className="text-muted-foreground font-mono text-xs">{n.host}</div>
                  </TableCell>
                  <TableCell>
                    <Badge variant={n.role === "control-plane" ? "info" : "secondary"}>{roleLabel(n.role)}</Badge>
                  </TableCell>
                  <TableCell className="text-sm">{n.osPrettyName || "—"}</TableCell>
                  <TableCell>
                    <CheckBadge status={n.lastCheckStatus} />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      {job ? (
        <>
          <div className="mb-3 flex items-center justify-between">
            <h2 className="text-lg font-semibold">Execução do deploy</h2>
            <Link to={`/jobs/${job.id}`} className="text-muted-foreground text-sm hover:underline">
              tentativa {job.attempts} · detalhes do job
            </Link>
          </div>
          {job.error && <div className="border-destructive/40 bg-destructive/10 text-destructive mb-4 rounded-lg border p-3 text-sm">{job.error}</div>}
          <JobRun key={job.id} jobId={job.id} names={names} />
        </>
      ) : (
        <p className="text-muted-foreground text-sm">O deploy ainda não foi iniciado.</p>
      )}
    </>
  );
}
