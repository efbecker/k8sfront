import { useMemo, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CloudDownload, Eraser, Loader2, Settings2, UploadCloud } from "lucide-react";
import { api, isFinal, type CatalogImage, type Job, type PruneNode, type PullState } from "@/lib/api";
import { formatBytes } from "@/lib/utils";
import { ClusterHeader, KubeError } from "@/components/cluster/ClusterHeader";
import { Tabs } from "@/components/cluster/NamespacePicker";
import { Empty } from "@/components/cluster/k8s";
import { ConfirmButton, ErrorText, JobLink } from "@/components/deploy/shared";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Textarea } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

type Filter = "all" | "used" | "unused";

const imageKinds = new Set(["cluster.registries-sync", "cluster.image-prepull", "cluster.image-prune"]);

function useImageJobs(id: string) {
  return useQuery({
    queryKey: ["jobs", "cluster", id],
    queryFn: () => api.jobs({ targetId: id, limit: 100 }),
    refetchInterval: (q) => ((q.state.data ?? []).some((j) => imageKinds.has(j.kind) && !isFinal(j.status)) ? 3_000 : 15_000),
  });
}

const latest = (jobs: Job[] | undefined, kind: string) => (jobs ?? []).find((j) => j.kind === kind);

function UseList({ im }: { im: CatalogImage }) {
  if (!im.inUse) return <span className="text-muted-foreground text-xs">nenhum pod</span>;
  const shown = im.uses.slice(0, 2);
  return (
    <div className="space-y-0.5 text-xs">
      {shown.map((u) => (
        <div key={`${u.namespace}/${u.kind}/${u.name}`} className="truncate" title={`${u.kind} ${u.namespace}/${u.name}`}>
          <span className="text-muted-foreground">{u.namespace}/</span>
          {u.name}
        </div>
      ))}
      {im.uses.length > shown.length && <div className="text-muted-foreground">+{im.uses.length - shown.length}</div>}
    </div>
  );
}

function PrepullResult({ job }: { job: Job }) {
  const res = job.result as { images?: string[]; states?: PullState[] } | undefined;
  const states = res?.states ?? [];
  if (!states.length) return null;
  const failed = states.filter((s) => s.state !== "pulled");
  return (
    <div className="text-sm">
      {failed.length === 0 ? (
        <p className="text-success">
          {res?.images?.length ?? 0} imagem(ns) em cache em {new Set(states.map((s) => s.node)).size} nó(s).
        </p>
      ) : (
        <ul className="space-y-1">
          {failed.map((s) => (
            <li key={s.node + s.image} className="text-destructive text-xs">
              <span className="font-mono">{s.image}</span> em {s.node}: {s.message || s.state}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function PruneResult({ job }: { job: Job }) {
  const nodes = (job.result as { nodes?: PruneNode[] } | undefined)?.nodes ?? [];
  if (!nodes.length) return null;
  return (
    <ul className="space-y-0.5 text-xs">
      {nodes.map((n) => (
        <li key={n.serverId} className={n.error ? "text-destructive" : undefined}>
          <span className="font-medium">{n.name}</span>:{" "}
          {n.error ? n.error : `${n.removed} removida(s), ${n.remaining} restante(s), ~${formatBytes(n.freedBytes) === "—" ? "0 MiB" : formatBytes(n.freedBytes)} liberados`}
        </li>
      ))}
    </ul>
  );
}

export function ClusterImagesPage() {
  const { id = "" } = useParams();
  const qc = useQueryClient();
  const catalog = useQuery({ queryKey: ["images", id], queryFn: () => api.images(id), refetchInterval: 30_000 });
  const registries = useQuery({ queryKey: ["registries"], queryFn: api.registries });
  const jobs = useImageJobs(id);
  const [filter, setFilter] = useState<Filter>("all");
  const [q, setQ] = useState("");
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [extra, setExtra] = useState("");
  const onJob = () => {
    qc.invalidateQueries({ queryKey: ["jobs", "cluster", id] });
  };
  const sync = useMutation({ mutationFn: () => api.syncRegistries(id), onSuccess: onJob });
  const prepull = useMutation({
    mutationFn: (images: string[]) => api.prepullImages(id, images),
    onSuccess: () => {
      setSelected(new Set());
      setExtra("");
      onJob();
    },
  });
  const prune = useMutation({ mutationFn: () => api.pruneImages(id), onSuccess: onJob });

  const images = useMemo(() => catalog.data?.images ?? [], [catalog.data]);
  const totalNodes = catalog.data?.nodes.length ?? 0;
  const shown = images.filter(
    (im) => (filter === "all" || (filter === "used" ? im.inUse : !im.inUse)) && (!q || im.ref.toLowerCase().includes(q.toLowerCase())),
  );
  const unused = images.filter((im) => !im.inUse);
  const unusedBytes = unused.reduce((a, im) => a + im.sizeBytes * im.nodes.length, 0);
  const regsHere = (registries.data ?? []).filter((r) => r.clusterIds.length === 0 || r.clusterIds.includes(id));
  const toPull = [...selected, ...extra.split(/[\s,]+/).filter(Boolean)].filter((v, i, a) => a.indexOf(v) === i);
  const lastSync = latest(jobs.data, "cluster.registries-sync");
  const lastPull = latest(jobs.data, "cluster.image-prepull");
  const lastPrune = latest(jobs.data, "cluster.image-prune");
  const toggle = (ref: string, on: boolean) =>
    setSelected((s) => {
      const n = new Set(s);
      if (on) n.add(ref);
      else n.delete(ref);
      return n;
    });

  return (
    <>
      <ClusterHeader id={id} />
      <div className="mb-6 grid gap-6 xl:grid-cols-3">
        <Card>
          <CardHeader>
            <CardTitle>Registries</CardTitle>
            <CardDescription>Mirrors, CAs e imagePullSecrets aplicados a este cluster.</CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            {regsHere.length === 0 ? (
              <p className="text-muted-foreground text-sm">Nenhum registry cadastrado para este cluster.</p>
            ) : (
              <div className="flex flex-wrap gap-1">
                {regsHere.map((r) => (
                  <Badge key={r.id} variant="secondary" title={r.endpoint}>
                    {r.name} · {r.host}
                    {r.mirrors.length > 0 && ` · ${r.mirrors.length} mirror(s)`}
                  </Badge>
                ))}
              </div>
            )}
            <div className="flex flex-wrap gap-2">
              <Button requires="operator" size="sm" disabled={sync.isPending || (lastSync && !isFinal(lastSync.status))} onClick={() => sync.mutate()}>
                <UploadCloud /> Aplicar neste cluster
              </Button>
              <Button size="sm" variant="outline" asChild>
                <Link to="/registries">
                  <Settings2 /> Gerenciar registries
                </Link>
              </Button>
            </div>
            <ErrorText error={sync.error} />
            {lastSync && <JobLink job={lastSync} />}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Pré-pull em todos os nós</CardTitle>
            <CardDescription>
              Um DaemonSet temporário em <code>k8sfront-system</code> baixa as imagens em cada nó usando os imagePullSecrets (nenhuma credencial vai para a linha de
              comando dos nós) e é removido no fim.
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            <Textarea
              className="min-h-16"
              value={extra}
              onChange={(e) => setExtra(e.target.value)}
              placeholder={"ghcr.io/org/app:1.4.2\nnginx:1.29"}
              aria-label="Imagens para pré-pull"
            />
            {selected.size > 0 && <p className="text-muted-foreground text-xs">+ {selected.size} selecionada(s) na tabela abaixo.</p>}
            <Button requires="operator" size="sm" disabled={toPull.length === 0 || toPull.length > 20 || prepull.isPending} onClick={() => prepull.mutate(toPull)}>
              {prepull.isPending ? <Loader2 className="animate-spin" /> : <CloudDownload />} Baixar {toPull.length || ""} imagem(ns)
            </Button>
            {toPull.length > 20 && <p className="text-destructive text-xs">No máximo 20 imagens por vez.</p>}
            <ErrorText error={prepull.error} />
            {lastPull && (
              <>
                <JobLink job={lastPull} />
                {isFinal(lastPull.status) && <PrepullResult job={lastPull} />}
              </>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Limpar imagens sem uso</CardTitle>
            <CardDescription>
              <code>crictl rmi --prune</code> em cada nó: remove as imagens que nenhum container usa. Nada que está rodando é tocado.
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            <p className="text-sm">
              {unused.length} imagem(ns) em cache sem uso
              {unusedBytes > 0 && <span className="text-muted-foreground"> (~{formatBytes(unusedBytes)} somando os nós)</span>}.
            </p>
            <ConfirmButton
              size="sm"
              destructive
              disabled={prune.isPending || (lastPrune && !isFinal(lastPrune.status))}
              title="Limpar imagens sem uso em todos os nós?"
              description="Imagens baixadas por pré-pull e ainda não usadas também saem, e serão baixadas de novo quando um pod precisar delas."
              confirmLabel="Limpar"
              onConfirm={() => prune.mutate()}
            >
              <Eraser /> Limpar
            </ConfirmButton>
            <ErrorText error={prune.error} />
            {lastPrune && (
              <>
                <JobLink job={lastPrune} />
                {isFinal(lastPrune.status) && <PruneResult job={lastPrune} />}
              </>
            )}
          </CardContent>
        </Card>
      </div>

      <div className="mb-3 flex flex-wrap items-center gap-2">
        <Tabs
          value={filter}
          onChange={setFilter}
          items={[
            { id: "all", label: "Todas", count: images.length },
            { id: "used", label: "Em uso", count: images.length - unused.length },
            { id: "unused", label: "Sem uso", count: unused.length },
          ]}
        />
        <Input className="ml-auto h-8 w-full sm:w-72" placeholder="Filtrar por nome" value={q} onChange={(e) => setQ(e.target.value)} />
      </div>
      {catalog.error ? (
        <KubeError error={catalog.error} />
      ) : (
        <Card className="py-0">
          <CardContent className="px-0">
            {catalog.isLoading ? (
              <Loader2 className="text-muted-foreground m-6 animate-spin" />
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className="w-8" />
                    <TableHead>Imagem</TableHead>
                    <TableHead>Tamanho</TableHead>
                    <TableHead>Nós</TableHead>
                    <TableHead>Usada por</TableHead>
                    <TableHead>Estado</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {shown.map((im) => (
                    <TableRow key={im.ref}>
                      <TableCell>
                        <input
                          type="checkbox"
                          className="accent-primary size-4"
                          aria-label={`Selecionar ${im.ref} para pré-pull`}
                          checked={selected.has(im.ref)}
                          onChange={(e) => toggle(im.ref, e.target.checked)}
                        />
                      </TableCell>
                      <TableCell className="max-w-md">
                        <div className="truncate font-mono text-xs" title={im.ref}>
                          {im.ref}
                        </div>
                        <div className="mt-1 flex flex-wrap gap-1">
                          {im.registry ? <Badge variant="info">{im.registry}</Badge> : <Badge variant="outline">{im.host}</Badge>}
                        </div>
                      </TableCell>
                      <TableCell className="text-sm tabular-nums">{formatBytes(im.sizeBytes)}</TableCell>
                      <TableCell className="text-sm tabular-nums" title={im.nodes.join(", ")}>
                        {im.nodes.length}/{totalNodes}
                      </TableCell>
                      <TableCell className="max-w-56">
                        <UseList im={im} />
                      </TableCell>
                      <TableCell>
                        {im.system ? (
                          <Badge variant="secondary">sistema</Badge>
                        ) : im.inUse ? (
                          <Badge variant="success">em uso</Badge>
                        ) : (
                          <Badge variant="warning">sem uso</Badge>
                        )}
                      </TableCell>
                    </TableRow>
                  ))}
                  {shown.length === 0 && <Empty cols={6} />}
                </TableBody>
              </Table>
            )}
          </CardContent>
        </Card>
      )}
      {(catalog.data?.truncated.length ?? 0) > 0 && (
        <p className="text-muted-foreground mt-2 text-xs">
          O kubelet informa só as 50 maiores imagens de cada nó; {catalog.data!.truncated.join(", ")} pode(m) ter mais imagens em cache do que as listadas.
        </p>
      )}
    </>
  );
}
