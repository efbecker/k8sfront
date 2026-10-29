import { Link, Navigate } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { Boxes, Loader2, Plus, Rocket } from "lucide-react";
import { api } from "@/lib/api";
import { formatDate } from "@/lib/utils";
import { PageHeader } from "@/components/layout/AppShell";
import { ClusterStatusBadge } from "@/components/status";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

/** Landing route: the wizard when no cluster exists yet, otherwise the cluster list. */
export function HomeRedirect() {
  const clusters = useQuery({ queryKey: ["clusters"], queryFn: api.clusters });
  if (clusters.isLoading) return <Loader2 className="text-muted-foreground animate-spin" />;
  return <Navigate to={(clusters.data ?? []).length === 0 ? "/clusters/new" : "/clusters"} replace />;
}

export function ClustersPage() {
  const clusters = useQuery({ queryKey: ["clusters"], queryFn: api.clusters, refetchInterval: 5000 });
  const list = clusters.data ?? [];
  return (
    <>
      <PageHeader
        title="Clusters"
        description="Clusters Kubernetes provisionados com kubeadm sobre Debian 13+."
        actions={
          <Button asChild>
            <Link to="/clusters/new">
              <Plus /> Novo cluster
            </Link>
          </Button>
        }
      />
      <Card className="py-0">
        <CardContent className="px-0">
          {clusters.isLoading ? (
            <Loader2 className="text-muted-foreground m-6 animate-spin" />
          ) : list.length === 0 ? (
            <div className="flex flex-col items-center gap-3 p-12 text-center">
              <Boxes className="text-muted-foreground size-10" />
              <div className="font-medium">Nenhum cluster ainda</div>
              <Button asChild>
                <Link to="/clusters/new">
                  <Rocket /> Abrir o Wizard de Deploy
                </Link>
              </Button>
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Cluster</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Kubernetes</TableHead>
                  <TableHead>CNI</TableHead>
                  <TableHead>Nós</TableHead>
                  <TableHead>Endpoint</TableHead>
                  <TableHead>Criado</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {list.map((c) => {
                  const cps = c.nodes.filter((n) => n.role === "control-plane").length;
                  return (
                    <TableRow key={c.id}>
                      <TableCell>
                        <Link to={`/clusters/${c.id}`} className="font-medium hover:underline">
                          {c.name}
                        </Link>
                        <div className="text-muted-foreground text-xs">{c.topology === "single-node" ? "Single-node" : "Multi-node"}</div>
                      </TableCell>
                      <TableCell>
                        <ClusterStatusBadge status={c.status} />
                      </TableCell>
                      <TableCell className="font-mono text-xs">{c.kubernetesVersion}</TableCell>
                      <TableCell>
                        <Badge variant="outline">{c.cni === "cilium" ? "Cilium" : "Calico"}</Badge>
                      </TableCell>
                      <TableCell className="text-sm">
                        {cps} CP · {c.nodes.length - cps} worker
                      </TableCell>
                      <TableCell className="font-mono text-xs">{c.controlPlaneEndpoint || "—"}</TableCell>
                      <TableCell className="text-sm">{formatDate(c.createdAt)}</TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </>
  );
}
