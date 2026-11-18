import { useState } from "react";
import { useParams, useSearchParams } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { Loader2, Lock, Star, Trash2 } from "lucide-react";
import { api, type NamespaceUsage, type ObjectRef } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { DeleteCell, DeleteDialog } from "@/components/deploy/shared";
import { ClusterHeader, KubeError } from "@/components/cluster/ClusterHeader";
import { NamespacePicker, Tabs } from "@/components/cluster/NamespacePicker";
import { age, Empty } from "@/components/cluster/k8s";
import { LoadBalancerCard } from "@/components/cluster/Access";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

type Tab = "loadbalancer" | "namespaces" | "services" | "ingresses" | "pvcs" | "pvs" | "storageClasses";

const sum = (u: NamespaceUsage, kinds: string[]) => kinds.reduce((n, k) => n + (u.counts[k] ?? 0), 0);

function Count({ n, danger }: { n: number; danger?: boolean }) {
  return <span className={"tabular-nums " + (n === 0 ? "text-muted-foreground" : danger ? "text-warning font-medium" : "")}>{n}</span>;
}

/** Namespaces with what is in each; deleting one shows everything it takes along. */
function NamespacesTab({ clusterId }: { clusterId: string }) {
  const usage = useQuery({ queryKey: ["namespaces-usage", clusterId], queryFn: () => api.namespaceUsage(clusterId), refetchInterval: 15_000 });
  const [del, setDel] = useState<string>();
  if (usage.isLoading) return <Loader2 className="text-muted-foreground animate-spin" />;
  if (usage.error) return <KubeError error={usage.error} />;
  const list = usage.data ?? [];
  return (
    <Card className="py-0">
      <CardContent className="px-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Namespace</TableHead>
              <TableHead>Workloads</TableHead>
              <TableHead>Pods</TableHead>
              <TableHead>Services</TableHead>
              <TableHead>Ingresses</TableHead>
              <TableHead>PVCs</TableHead>
              <TableHead>Helm</TableHead>
              <TableHead>Outros</TableHead>
              <TableHead>Idade</TableHead>
              <TableHead className="w-12" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {list.map((u) => (
              <TableRow key={u.name}>
                <TableCell>
                  <div className="flex items-center gap-2 font-medium">
                    {u.name}
                    {u.system && <Badge variant="secondary">sistema</Badge>}
                    {u.phase === "Terminating" && <Badge variant="warning">excluindo</Badge>}
                    {!u.system && u.items.length === 0 && u.pods === 0 && <Badge variant="outline">vazio</Badge>}
                  </div>
                </TableCell>
                <TableCell>
                  <Count n={sum(u, ["Deployment", "StatefulSet", "DaemonSet", "CronJob", "Job"])} />
                </TableCell>
                <TableCell>
                  <Count n={u.pods} />
                </TableCell>
                <TableCell>
                  <Count n={sum(u, ["Service"])} />
                </TableCell>
                <TableCell>
                  <Count n={sum(u, ["Ingress"])} />
                </TableCell>
                <TableCell>
                  <Count n={sum(u, ["PersistentVolumeClaim"])} danger />
                </TableCell>
                <TableCell>
                  <Count n={sum(u, ["HelmRelease"])} />
                </TableCell>
                <TableCell>
                  <Count n={sum(u, ["ConfigMap", "Secret", "ServiceAccount", "Pod"])} />
                </TableCell>
                <TableCell className="text-muted-foreground text-sm">{age(u.created)}</TableCell>
                {u.system || u.phase === "Terminating" ? (
                  <TableCell />
                ) : (
                  <TableCell className="text-right">
                    <Button requires="operator" variant="ghost" size="icon" title={`Excluir namespace ${u.name}`} onClick={() => setDel(u.name)}>
                      <Trash2 className="text-destructive" />
                    </Button>
                  </TableCell>
                )}
              </TableRow>
            ))}
            {list.length === 0 && <Empty cols={10} />}
          </TableBody>
        </Table>
      </CardContent>
      {del && (
        <DeleteDialog
          clusterId={clusterId}
          request={{ objects: [{ apiVersion: "v1", kind: "Namespace", name: del }] }}
          title={`Excluir o namespace ${del}?`}
          confirmWord={del}
          open
          onOpenChange={(o) => !o && setDel(undefined)}
        />
      )}
    </Card>
  );
}

export function ClusterNetworkPage() {
  const { id = "" } = useParams();
  const [ns, setNs] = useState("");
  const [params] = useSearchParams();
  const [tab, setTab] = useState<Tab>(params.get("tab") === "loadbalancer" ? "loadbalancer" : "services");
  const [del, setDel] = useState<ObjectRef>();
  const data = useQuery({ queryKey: ["network", id, ns], queryFn: () => api.network(id, ns), refetchInterval: 15_000 });
  const d = data.data;
  const statusVariant = (s: string) => (s === "Bound" ? "success" : s === "Pending" || s === "Available" ? "warning" : s ? "destructive" : "secondary");

  return (
    <>
      <ClusterHeader id={id} />
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <Tabs
          value={tab}
          onChange={setTab}
          items={[
            { id: "namespaces", label: "Namespaces" },
            { id: "services", label: "Services", count: d?.services.length },
            { id: "ingresses", label: "Ingresses", count: d?.ingresses.length },
            { id: "pvcs", label: "PVCs", count: d?.pvcs.length },
            { id: "pvs", label: "Volumes (PV)", count: d?.pvs.length },
            { id: "storageClasses", label: "StorageClasses", count: d?.storageClasses.length },
            { id: "loadbalancer", label: "LoadBalancer" },
          ]}
        />
        {tab !== "namespaces" && tab !== "loadbalancer" && (
          <div className="ml-auto flex items-center gap-3">
            {d && <span className="text-muted-foreground text-xs">{d.networkPolicies} NetworkPolicies</span>}
            <NamespacePicker clusterId={id} value={ns} onChange={setNs} />
          </div>
        )}
      </div>
      {tab === "loadbalancer" ? (
        <LoadBalancerCard clusterId={id} />
      ) : tab === "namespaces" ? (
        <NamespacesTab clusterId={id} />
      ) : data.isLoading ? (
        <Loader2 className="text-muted-foreground animate-spin" />
      ) : data.error ? (
        <KubeError error={data.error} />
      ) : (
        d && (
          <Card className="py-0">
            <CardContent className="px-0">
              {tab === "services" && (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Service</TableHead>
                      <TableHead>Tipo</TableHead>
                      <TableHead>Cluster IP</TableHead>
                      <TableHead>IP externo</TableHead>
                      <TableHead>Portas</TableHead>
                      <TableHead>Seletor</TableHead>
                      <TableHead>Idade</TableHead>
                      <TableHead className="w-12" />
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {d.services.map((s) => (
                      <TableRow key={s.uid}>
                        <TableCell>
                          <div className="font-medium">{s.name}</div>
                          <div className="text-muted-foreground text-xs">{s.namespace}</div>
                        </TableCell>
                        <TableCell>
                          <Badge variant={s.type === "ClusterIP" ? "secondary" : "info"}>{s.type}</Badge>
                        </TableCell>
                        <TableCell className="font-mono text-xs">{s.clusterIP || "—"}</TableCell>
                        <TableCell className="font-mono text-xs">
                          {s.externalIPs?.join(", ") ||
                            (s.type === "LoadBalancer" ? (
                              <Badge variant="warning" title="Nenhum provedor entregou IP: veja a aba LoadBalancer">
                                Pending
                              </Badge>
                            ) : (
                              "—"
                            ))}
                        </TableCell>
                        <TableCell className="font-mono text-xs">{s.ports.join(", ") || "—"}</TableCell>
                        <TableCell className="max-w-56 font-mono text-[11px] break-all whitespace-normal">
                          {Object.entries(s.selector ?? {})
                            .map(([k, v]) => `${k}=${v}`)
                            .join(", ") || "—"}
                        </TableCell>
                        <TableCell className="text-muted-foreground text-sm">{age(s.created)}</TableCell>
                        <DeleteCell target={{ apiVersion: "v1", kind: "Service", namespace: s.namespace, name: s.name }} onDelete={setDel} />
                      </TableRow>
                    ))}
                    {d.services.length === 0 && <Empty cols={8} />}
                  </TableBody>
                </Table>
              )}
              {tab === "ingresses" && (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Ingress</TableHead>
                      <TableHead>Classe</TableHead>
                      <TableHead>Hosts</TableHead>
                      <TableHead>Endereço</TableHead>
                      <TableHead>Idade</TableHead>
                      <TableHead className="w-12" />
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {d.ingresses.map((i) => (
                      <TableRow key={`${i.namespace}/${i.name}`}>
                        <TableCell>
                          <div className="flex items-center gap-1 font-medium">
                            {i.name} {i.tls && <Lock className="text-success size-3.5" aria-label="TLS" />}
                          </div>
                          <div className="text-muted-foreground text-xs">{i.namespace}</div>
                        </TableCell>
                        <TableCell>{i.class || "—"}</TableCell>
                        <TableCell className="font-mono text-xs">{i.hosts.join(", ") || "*"}</TableCell>
                        <TableCell className="font-mono text-xs">{i.address.join(", ") || "—"}</TableCell>
                        <TableCell className="text-muted-foreground text-sm">{age(i.created)}</TableCell>
                        <DeleteCell target={{ apiVersion: "networking.k8s.io/v1", kind: "Ingress", namespace: i.namespace, name: i.name }} onDelete={setDel} />
                      </TableRow>
                    ))}
                    {d.ingresses.length === 0 && <Empty cols={6} />}
                  </TableBody>
                </Table>
              )}
              {tab === "pvcs" && (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Claim</TableHead>
                      <TableHead>Estado</TableHead>
                      <TableHead>Capacidade</TableHead>
                      <TableHead>StorageClass</TableHead>
                      <TableHead>Acesso</TableHead>
                      <TableHead>Volume</TableHead>
                      <TableHead>Idade</TableHead>
                      <TableHead className="w-12" />
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {d.pvcs.map((c) => (
                      <TableRow key={`${c.namespace}/${c.name}`}>
                        <TableCell>
                          <div className="font-medium">{c.name}</div>
                          <div className="text-muted-foreground text-xs">{c.namespace}</div>
                        </TableCell>
                        <TableCell>
                          <Badge variant={statusVariant(c.status)}>{c.status}</Badge>
                        </TableCell>
                        <TableCell>{c.capacity || "—"}</TableCell>
                        <TableCell>{c.storageClass || "—"}</TableCell>
                        <TableCell className="text-xs">{c.accessModes.join(", ")}</TableCell>
                        <TableCell className="font-mono text-xs">{c.volume || "—"}</TableCell>
                        <TableCell className="text-muted-foreground text-sm">{age(c.created)}</TableCell>
                        <DeleteCell target={{ apiVersion: "v1", kind: "PersistentVolumeClaim", namespace: c.namespace, name: c.name }} onDelete={setDel} />
                      </TableRow>
                    ))}
                    {d.pvcs.length === 0 && <Empty cols={8} />}
                  </TableBody>
                </Table>
              )}
              {tab === "pvs" && (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Volume</TableHead>
                      <TableHead>Estado</TableHead>
                      <TableHead>Capacidade</TableHead>
                      <TableHead>Claim</TableHead>
                      <TableHead>StorageClass</TableHead>
                      <TableHead>Reclaim</TableHead>
                      <TableHead>Idade</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {d.pvs.map((p) => (
                      <TableRow key={p.name}>
                        <TableCell className="font-medium">{p.name}</TableCell>
                        <TableCell>
                          <Badge variant={statusVariant(p.status)}>{p.status}</Badge>
                        </TableCell>
                        <TableCell>{p.capacity || "—"}</TableCell>
                        <TableCell className="font-mono text-xs">{p.claim || "—"}</TableCell>
                        <TableCell>{p.storageClass || "—"}</TableCell>
                        <TableCell>{p.reclaimPolicy}</TableCell>
                        <TableCell className="text-muted-foreground text-sm">{age(p.created)}</TableCell>
                      </TableRow>
                    ))}
                    {d.pvs.length === 0 && <Empty cols={7} />}
                  </TableBody>
                </Table>
              )}
              {tab === "storageClasses" && (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>StorageClass</TableHead>
                      <TableHead>Provisionador</TableHead>
                      <TableHead>Reclaim</TableHead>
                      <TableHead>Binding</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {d.storageClasses.map((s) => (
                      <TableRow key={s.name}>
                        <TableCell className="font-medium">
                          <span className="flex items-center gap-1">
                            {s.name}
                            {s.default && (
                              <Badge variant="info">
                                <Star /> padrão
                              </Badge>
                            )}
                          </span>
                        </TableCell>
                        <TableCell className="font-mono text-xs">{s.provisioner}</TableCell>
                        <TableCell>{s.reclaimPolicy}</TableCell>
                        <TableCell>{s.bindingMode}</TableCell>
                      </TableRow>
                    ))}
                    {d.storageClasses.length === 0 && (
                      <TableRow>
                        <TableCell colSpan={4} className="text-muted-foreground py-8 text-center text-sm">
                          Nenhuma StorageClass. Volumes persistentes precisam de um provisionador (ex.: local-path, NFS, Longhorn), que pode ser
                          instalado pela aba Helm da Central de Deploy.
                        </TableCell>
                      </TableRow>
                    )}
                  </TableBody>
                </Table>
              )}
            </CardContent>
          </Card>
        )
      )}
      {del && (
        <DeleteDialog
          clusterId={id}
          request={{ objects: [del] }}
          title={`Excluir ${del.kind} ${del.namespace}/${del.name}?`}
          confirmWord={del.name}
          open
          onOpenChange={(o) => !o && setDel(undefined)}
        />
      )}
    </>
  );
}
