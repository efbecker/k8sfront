import { useState } from "react";
import { useParams } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { Loader2, Lock, Star } from "lucide-react";
import { api } from "@/lib/api";
import { ClusterHeader, KubeError } from "@/components/cluster/ClusterHeader";
import { NamespacePicker, Tabs } from "@/components/cluster/NamespacePicker";
import { age, Empty } from "@/components/cluster/k8s";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

type Tab = "services" | "ingresses" | "pvcs" | "pvs" | "storageClasses";

export function ClusterNetworkPage() {
  const { id = "" } = useParams();
  const [ns, setNs] = useState("");
  const [tab, setTab] = useState<Tab>("services");
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
            { id: "services", label: "Services", count: d?.services.length },
            { id: "ingresses", label: "Ingresses", count: d?.ingresses.length },
            { id: "pvcs", label: "PVCs", count: d?.pvcs.length },
            { id: "pvs", label: "Volumes (PV)", count: d?.pvs.length },
            { id: "storageClasses", label: "StorageClasses", count: d?.storageClasses.length },
          ]}
        />
        <div className="ml-auto flex items-center gap-3">
          {d && <span className="text-muted-foreground text-xs">{d.networkPolicies} NetworkPolicies</span>}
          <NamespacePicker clusterId={id} value={ns} onChange={setNs} />
        </div>
      </div>
      {data.isLoading ? (
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
                        <TableCell className="font-mono text-xs">{s.externalIPs?.join(", ") || "—"}</TableCell>
                        <TableCell className="font-mono text-xs">{s.ports.join(", ") || "—"}</TableCell>
                        <TableCell className="max-w-56 font-mono text-[11px] break-all whitespace-normal">
                          {Object.entries(s.selector ?? {})
                            .map(([k, v]) => `${k}=${v}`)
                            .join(", ") || "—"}
                        </TableCell>
                        <TableCell className="text-muted-foreground text-sm">{age(s.created)}</TableCell>
                      </TableRow>
                    ))}
                    {d.services.length === 0 && <Empty cols={7} />}
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
                      </TableRow>
                    ))}
                    {d.ingresses.length === 0 && <Empty cols={5} />}
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
                      </TableRow>
                    ))}
                    {d.pvcs.length === 0 && <Empty cols={7} />}
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
                          Nenhuma StorageClass. Volumes persistentes precisam de um provisionador (ex.: local-path, NFS, Longhorn), que entra na
                          Central de Deploy (Fase 4).
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
    </>
  );
}
