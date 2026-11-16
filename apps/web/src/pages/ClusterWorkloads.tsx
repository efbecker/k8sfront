import { useState } from "react";
import { useParams } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { Loader2 } from "lucide-react";
import { api, type K8sWorkload } from "@/lib/api";
import { ClusterHeader, KubeError } from "@/components/cluster/ClusterHeader";
import { NamespacePicker, Tabs } from "@/components/cluster/NamespacePicker";
import { age, Empty, PodBadge } from "@/components/cluster/k8s";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

type Tab = "Deployment" | "StatefulSet" | "DaemonSet" | "Pod";

function ReadyBadge({ w }: { w: K8sWorkload }) {
  const ok = w.ready >= w.desired;
  return (
    <Badge variant={ok ? "success" : w.ready === 0 && w.desired > 0 ? "destructive" : "warning"} className="tabular-nums">
      {w.ready}/{w.desired}
    </Badge>
  );
}

export function ClusterWorkloadsPage() {
  const { id = "" } = useParams();
  const [ns, setNs] = useState("");
  const [tab, setTab] = useState<Tab>("Deployment");
  const [q, setQ] = useState("");
  const data = useQuery({ queryKey: ["workloads", id, ns], queryFn: () => api.workloads(id, ns), refetchInterval: 10_000 });
  const filter = (name: string) => !q || name.toLowerCase().includes(q.toLowerCase());
  const wl = (data.data?.workloads ?? []).filter((w) => w.kind === tab && filter(w.name));
  const pods = (data.data?.pods ?? []).filter((p) => filter(p.name));
  const count = (k: Tab) => (k === "Pod" ? data.data?.pods.length : data.data?.workloads.filter((w) => w.kind === k).length);

  return (
    <>
      <ClusterHeader id={id} />
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <Tabs
          value={tab}
          onChange={setTab}
          items={(["Deployment", "StatefulSet", "DaemonSet", "Pod"] as Tab[]).map((k) => ({ id: k, label: k === "Pod" ? "Pods" : `${k}s`, count: count(k) }))}
        />
        <div className="ml-auto flex flex-wrap gap-2">
          <Input className="h-9 w-52" placeholder="Filtrar por nome" value={q} onChange={(e) => setQ(e.target.value)} />
          <NamespacePicker clusterId={id} value={ns} onChange={setNs} />
        </div>
      </div>
      {data.isLoading ? (
        <Loader2 className="text-muted-foreground animate-spin" />
      ) : data.error ? (
        <KubeError error={data.error} />
      ) : (
        <Card className="py-0">
          <CardContent className="px-0">
            {tab === "Pod" ? (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Pod</TableHead>
                    <TableHead>Estado</TableHead>
                    <TableHead>Prontos</TableHead>
                    <TableHead>Reinícios</TableHead>
                    <TableHead>Nó</TableHead>
                    <TableHead>IP</TableHead>
                    <TableHead>Idade</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {pods.map((p) => (
                    <TableRow key={p.uid}>
                      <TableCell>
                        <div className="font-medium">{p.name}</div>
                        <div className="text-muted-foreground text-xs">
                          {p.namespace}
                          {p.ownerKind && ` · ${p.ownerKind}/${p.ownerName}`}
                        </div>
                      </TableCell>
                      <TableCell>
                        <PodBadge p={p} />
                      </TableCell>
                      <TableCell className="tabular-nums">
                        {p.ready}/{p.containers}
                      </TableCell>
                      <TableCell className={p.restarts ? "text-warning tabular-nums" : "tabular-nums"}>{p.restarts}</TableCell>
                      <TableCell className="text-sm">{p.node || "—"}</TableCell>
                      <TableCell className="font-mono text-xs">{p.ip || "—"}</TableCell>
                      <TableCell className="text-muted-foreground text-sm">{age(p.created)}</TableCell>
                    </TableRow>
                  ))}
                  {pods.length === 0 && <Empty cols={7} />}
                </TableBody>
              </Table>
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Nome</TableHead>
                    <TableHead>Prontos</TableHead>
                    <TableHead>Atualizados</TableHead>
                    <TableHead>Disponíveis</TableHead>
                    <TableHead>Imagens</TableHead>
                    <TableHead>Idade</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {wl.map((w) => (
                    <TableRow key={`${w.namespace}/${w.name}`}>
                      <TableCell>
                        <div className="font-medium">{w.name}</div>
                        <div className="text-muted-foreground text-xs">{w.namespace}</div>
                      </TableCell>
                      <TableCell>
                        <ReadyBadge w={w} />
                      </TableCell>
                      <TableCell className="tabular-nums">{w.updated}</TableCell>
                      <TableCell className="tabular-nums">{w.available}</TableCell>
                      <TableCell className="max-w-sm">
                        {w.images.map((i) => (
                          <div key={i} className="truncate font-mono text-xs" title={i}>
                            {i}
                          </div>
                        ))}
                      </TableCell>
                      <TableCell className="text-muted-foreground text-sm">{age(w.created)}</TableCell>
                    </TableRow>
                  ))}
                  {wl.length === 0 && <Empty cols={6} />}
                </TableBody>
              </Table>
            )}
          </CardContent>
        </Card>
      )}
    </>
  );
}
