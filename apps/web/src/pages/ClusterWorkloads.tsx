import { useState } from "react";
import { useParams } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { Loader2 } from "lucide-react";
import { api, type K8sWorkload, type ObjectRef } from "@/lib/api";
import { DeleteCell, DeleteDialog } from "@/components/deploy/shared";
import { ClusterHeader, KubeError } from "@/components/cluster/ClusterHeader";
import { NamespacePicker, Tabs } from "@/components/cluster/NamespacePicker";
import { age, Empty, PodBadge } from "@/components/cluster/k8s";
import { AccessPanel } from "@/components/cluster/Access";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

type Tab = "Deployment" | "StatefulSet" | "DaemonSet" | "Pod" | "Access";

function ReadyBadge({ w }: { w: K8sWorkload }) {
  const ok = w.ready >= w.desired;
  return (
    <Badge variant={ok ? "success" : w.ready === 0 && w.desired > 0 ? "destructive" : "warning"} className="tabular-nums">
      {w.ready}/{w.desired}
    </Badge>
  );
}

/** Deleting a workload offers the Services/Ingresses that only serve it. */
function WorkloadDelete({ clusterId, target, onClose }: { clusterId: string; target: ObjectRef; onClose: () => void }) {
  const isPod = target.kind === "Pod";
  const related = useQuery({
    queryKey: ["related", clusterId, target.namespace, target.kind, target.name],
    queryFn: () => api.workloadRelated(clusterId, target.namespace ?? "", target.kind, target.name),
    enabled: !isPod,
  });
  const [withRelated, setWithRelated] = useState(true);
  const rel = related.data ?? [];
  const objects = withRelated ? [target, ...rel] : [target];
  return (
    <DeleteDialog
      clusterId={clusterId}
      request={{ objects }}
      title={`Excluir ${target.kind} ${target.namespace}/${target.name}?`}
      confirmWord={target.name}
      open
      onOpenChange={(o) => !o && onClose()}
      extra={
        rel.length > 0 && (
          <label className="flex cursor-pointer items-start gap-2 rounded-md border px-3 py-2 text-sm">
            <input type="checkbox" className="accent-primary mt-0.5 size-4" checked={withRelated} onChange={(e) => setWithRelated(e.target.checked)} />
            <span>
              Excluir também o que só serve a este {target.kind}: {rel.map((r) => `${r.kind} ${r.name}`).join(", ")}
            </span>
          </label>
        )
      }
    />
  );
}

export function ClusterWorkloadsPage() {
  const { id = "" } = useParams();
  const [ns, setNs] = useState("");
  const [tab, setTab] = useState<Tab>("Deployment");
  const [q, setQ] = useState("");
  const [del, setDel] = useState<ObjectRef>();
  const data = useQuery({ queryKey: ["workloads", id, ns], queryFn: () => api.workloads(id, ns), refetchInterval: 10_000 });
  const filter = (name: string) => !q || name.toLowerCase().includes(q.toLowerCase());
  const wl = (data.data?.workloads ?? []).filter((w) => w.kind === tab && filter(w.name));
  const pods = (data.data?.pods ?? []).filter((p) => filter(p.name));
  const count = (k: Tab) => (k === "Access" ? undefined : k === "Pod" ? data.data?.pods.length : data.data?.workloads.filter((w) => w.kind === k).length);

  return (
    <>
      <ClusterHeader id={id} />
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <Tabs
          value={tab}
          onChange={setTab}
          items={[
            ...(["Deployment", "StatefulSet", "DaemonSet", "Pod"] as Tab[]).map((k) => ({ id: k, label: k === "Pod" ? "Pods" : `${k}s`, count: count(k) })),
            { id: "Access" as Tab, label: "Acesso" },
          ]}
        />
        <div className="ml-auto flex flex-wrap gap-2">
          {tab !== "Access" && <Input className="h-9 w-52" placeholder="Filtrar por nome" value={q} onChange={(e) => setQ(e.target.value)} />}
          <NamespacePicker clusterId={id} value={ns} onChange={setNs} />
        </div>
      </div>
      {tab === "Access" ? (
        <AccessPanel clusterId={id} namespace={ns} />
      ) : data.isLoading ? (
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
                    <TableHead className="w-12" />
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
                      <DeleteCell target={{ apiVersion: "v1", kind: "Pod", namespace: p.namespace, name: p.name }} onDelete={setDel} />
                    </TableRow>
                  ))}
                  {pods.length === 0 && <Empty cols={8} />}
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
                    <TableHead className="w-12" />
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
                      <DeleteCell target={{ apiVersion: "apps/v1", kind: w.kind, namespace: w.namespace, name: w.name }} onDelete={setDel} />
                    </TableRow>
                  ))}
                  {wl.length === 0 && <Empty cols={7} />}
                </TableBody>
              </Table>
            )}
          </CardContent>
        </Card>
      )}
      {del && <WorkloadDelete clusterId={id} target={del} onClose={() => setDel(undefined)} />}
    </>
  );
}
