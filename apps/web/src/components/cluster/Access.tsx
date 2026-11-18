import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CircleAlert, CircleCheck, ExternalLink, Globe, Loader2, Network, Radar, ShieldAlert } from "lucide-react";
import { api, isFinal, type AccessLink, type AccessList, type Job, type ProbeResult, type ServiceAccess } from "@/lib/api";
import { ConfirmButton, ErrorText, JobLink } from "@/components/deploy/shared";
import { KubeError } from "@/components/cluster/ClusterHeader";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

const kindLabel: Record<AccessLink["kind"], string> = {
  loadbalancer: "LoadBalancer",
  "external-ip": "externalIP",
  nodeport: "NodePort",
  ingress: "Ingress",
};

function ProbeBadge({ r }: { r: ProbeResult }) {
  if (r.ok)
    return (
      <Badge variant="success" title={r.insecure ? "Certificado TLS não verificado" : undefined}>
        <CircleCheck /> {r.status} · {r.latencyMs} ms
      </Badge>
    );
  return (
    <Badge variant="destructive" className="max-w-full whitespace-normal" title={r.error}>
      <CircleAlert /> {r.status ? `${r.status}: ` : ""}
      {r.error}
    </Badge>
  );
}

function LinkRow({ clusterId, svc, link }: { clusterId: string; svc: ServiceAccess; link: AccessLink }) {
  const probe = useMutation({ mutationFn: () => api.probeAccess(clusterId, svc.namespace, svc.name, link.url) });
  return (
    <div className="flex flex-wrap items-center gap-2 py-1">
      <Badge variant={link.kind === "nodeport" ? "secondary" : "info"} className="w-24">
        {kindLabel[link.kind]}
      </Badge>
      <a href={link.url} target="_blank" rel="noreferrer noopener" className="text-primary inline-flex items-center gap-1 font-mono text-sm break-all hover:underline">
        {link.url} <ExternalLink className="size-3.5 shrink-0" />
      </a>
      {link.node && <span className="text-muted-foreground text-xs">nó {link.node}</span>}
      <Button variant="ghost" size="sm" onClick={() => probe.mutate()} disabled={probe.isPending} title="Faz um GET a partir do servidor do k8sfront">
        {probe.isPending ? <Loader2 className="animate-spin" /> : <Radar />} Testar
      </Button>
      {probe.data && <ProbeBadge r={probe.data} />}
      <ErrorText error={probe.error} />
    </div>
  );
}

function PendingNotice({ clusterId, lb }: { clusterId: string; lb: AccessList["loadBalancer"] }) {
  return (
    <div className="border-warning/40 bg-warning/10 flex items-start gap-2 rounded-md border px-3 py-2 text-sm">
      <ShieldAlert className="text-warning mt-0.5 size-4 shrink-0" />
      {lb.installed ? (
        <span>
          IP externo <b>Pending</b>: o MetalLB ainda não entregou um IP. Isso leva alguns segundos; se continuar, a faixa{" "}
          {lb.addresses?.length ? <code>{lb.addresses.join(", ")}</code> : ""} pode estar esgotada (veja os eventos do Service).
        </span>
      ) : (
        <span>
          IP externo <b>Pending</b>: em servidores próprios não existe provedor de nuvem para entregar IPs de LoadBalancer, então o Service fica esperando para
          sempre. Ative o MetalLB com uma faixa de IPs livres da sua rede em{" "}
          <Link to={`/clusters/${clusterId}/network?tab=loadbalancer`} className="text-primary font-medium hover:underline">
            Rede &amp; Storage → LoadBalancer
          </Link>
          . Enquanto isso, a NodePort abaixo já funciona.
        </span>
      )}
    </div>
  );
}

function ServiceBlock({ clusterId, svc, lb }: { clusterId: string; svc: ServiceAccess; lb: AccessList["loadBalancer"] }) {
  const [allNodes, setAllNodes] = useState(false);
  const nodePorts = svc.links.filter((l) => l.kind === "nodeport");
  const others = svc.links.filter((l) => l.kind !== "nodeport");
  // One NodePort link per Service port; the other nodes answer the same.
  const firstPerPort = nodePorts.filter((l, i) => nodePorts.findIndex((o) => o.port === l.port) === i);
  const shownNodePorts = allNodes ? nodePorts : firstPerPort;
  const hidden = nodePorts.length - firstPerPort.length;
  return (
    <div className="space-y-2 px-6 py-4">
      <div className="flex flex-wrap items-center gap-2">
        <Globe className="text-primary size-4" />
        <span className="font-medium">{svc.name}</span>
        <span className="text-muted-foreground text-xs">{svc.namespace}</span>
        <Badge variant="outline">{svc.type}</Badge>
        {svc.endpoints === 0 ? (
          <Badge variant="destructive" title="Nenhum pod pronto atende este Service: os links não vão responder">
            sem pods prontos
          </Badge>
        ) : svc.endpoints > 0 ? (
          <Badge variant="success">{svc.endpoints} pod(s) pronto(s)</Badge>
        ) : null}
        {svc.localTraffic && (
          <Badge variant="warning" title="externalTrafficPolicy: Local">
            só responde em nós com pod
          </Badge>
        )}
      </div>
      {svc.pending && <PendingNotice clusterId={clusterId} lb={lb} />}
      <div className="divide-y">
        {others.map((l) => (
          <LinkRow key={l.url} clusterId={clusterId} svc={svc} link={l} />
        ))}
        {shownNodePorts.map((l) => (
          <LinkRow key={l.url} clusterId={clusterId} svc={svc} link={l} />
        ))}
      </div>
      {hidden > 0 && (
        <button className="text-muted-foreground hover:text-foreground text-xs" onClick={() => setAllNodes((v) => !v)}>
          {allNodes ? "Mostrar só um nó" : `A NodePort responde em qualquer nó: mostrar os outros ${hidden} endereço(s)`}
        </button>
      )}
    </div>
  );
}

/**
 * External links of Services (LoadBalancer IP, NodePort on each node, Ingress
 * hosts) with a reachability test run from the k8sfront server. `only`
 * restricts the panel to the given Services (e.g. those just applied).
 */
export function AccessPanel({
  clusterId,
  namespace = "",
  only,
  title = "Acesso às aplicações",
  emptyText,
}: {
  clusterId: string;
  namespace?: string;
  only?: { namespace?: string; name: string }[];
  title?: string;
  emptyText?: string;
}) {
  const nss = only ? [...new Set(only.map((o) => o.namespace ?? "default"))] : [];
  const ns = only ? (nss.length === 1 ? nss[0] : "") : namespace;
  const data = useQuery({
    queryKey: ["access", clusterId, ns],
    queryFn: () => api.access(clusterId, ns),
    // Faster while a LoadBalancer waits for its IP.
    refetchInterval: (q) => ((q.state.data?.services ?? []).some((s) => s.pending) ? 4_000 : 15_000),
  });
  if (data.isLoading) return <Loader2 className="text-muted-foreground animate-spin" />;
  if (data.error) return <KubeError error={data.error} />;
  const acc = data.data!;
  const list = only ? acc.services.filter((s) => only.some((o) => (o.namespace ?? "default") === s.namespace && o.name === s.name)) : acc.services;
  return (
    <Card className="gap-0 pb-0">
      <CardHeader className="pb-4">
        <CardTitle className="flex items-center gap-2">
          <Network className="text-primary size-4" /> {title}
        </CardTitle>
        <CardDescription>
          Links por Service: IP do LoadBalancer, NodePort em cada nó e hosts de Ingress. “Testar” faz um GET a partir do servidor do k8sfront (qualquer resposta
          HTTP abaixo de 500 conta como acessível).
        </CardDescription>
      </CardHeader>
      <CardContent className="divide-y border-t px-0">
        {list.map((s) => (
          <ServiceBlock key={`${s.namespace}/${s.name}`} clusterId={clusterId} svc={s} lb={acc.loadBalancer} />
        ))}
        {list.length === 0 && (
          <p className="text-muted-foreground px-6 py-6 text-sm">
            {emptyText ??
              "Nenhum Service com acesso externo. Services ClusterIP só são acessíveis de dentro do cluster: use type NodePort ou LoadBalancer, ou um Ingress."}
          </p>
        )}
      </CardContent>
    </Card>
  );
}

/** Installs MetalLB (L2) or updates its address pool on an existing cluster. */
export function LoadBalancerCard({ clusterId }: { clusterId: string }) {
  const qc = useQueryClient();
  const cluster = useQuery({ queryKey: ["cluster", clusterId], queryFn: () => api.cluster(clusterId) });
  const access = useQuery({ queryKey: ["access", clusterId, ""], queryFn: () => api.access(clusterId, "") });
  const jobs = useQuery({
    queryKey: ["jobs", "cluster", clusterId],
    queryFn: () => api.jobs({ targetId: clusterId, limit: 100 }),
    refetchInterval: (q) => ((q.state.data ?? []).some((j) => !isFinal(j.status)) ? 3_000 : 15_000),
  });
  const current = cluster.data?.options.loadBalancer;
  const [text, setText] = useState<string>();
  const value = text ?? current?.addresses.join(", ") ?? "";
  const addresses = value
    .split(/[\s,;]+/)
    .map((a) => a.trim())
    .filter(Boolean);
  const [job, setJob] = useState<Job>();
  const run = useMutation({
    mutationFn: () => api.setLoadBalancer(clusterId, { provider: "metallb", addresses }),
    onSuccess: (j) => {
      setJob(j);
      qc.invalidateQueries({ queryKey: ["jobs", "cluster", clusterId] });
    },
  });
  const lastJob = (jobs.data ?? []).find((j) => j.kind === "cluster.loadbalancer");
  const running = lastJob && !isFinal(lastJob.status) ? lastJob : undefined;
  const lastDone = lastJob?.status === "succeeded" ? lastJob.id : undefined;
  useEffect(() => {
    // The job records the pool in the cluster options when it finishes.
    if (lastDone) {
      qc.invalidateQueries({ queryKey: ["cluster", clusterId] });
      qc.invalidateQueries({ queryKey: ["access", clusterId] });
    }
  }, [lastDone, clusterId, qc]);
  const installed = access.data?.loadBalancer.installed;
  const pending = (access.data?.services ?? []).filter((s) => s.pending).length;
  const cni = cluster.data?.cni;

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Network className="text-primary size-4" /> LoadBalancer (MetalLB, modo L2)
        </CardTitle>
        <CardDescription>
          Em servidores próprios, Services do tipo LoadBalancer ficam com IP externo Pending porque não há provedor de nuvem. O MetalLB entrega IPs de uma faixa
          da rede dos nós e responde ARP por eles a partir de um nó (failover automático se esse nó cair). Funciona com Calico e com Cilium
          {cni === "cilium" ? " (o L2 do próprio Cilium exigiria trocar o kube-proxy pelo eBPF do Cilium)" : ""}.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex flex-wrap items-center gap-2 text-sm">
          {current ? (
            <Badge variant="success">Configurado: {current.addresses.join(", ")}</Badge>
          ) : installed ? (
            <Badge variant="info">MetalLB detectado (instalado fora do k8sfront)</Badge>
          ) : (
            <Badge variant="secondary">Não configurado</Badge>
          )}
          {pending > 0 && <Badge variant="warning">{pending} Service(s) LoadBalancer aguardando IP</Badge>}
        </div>
        <div className="space-y-1.5">
          <Label>Faixa de IPs livres da rede dos nós</Label>
          <Input
            value={value}
            onChange={(e) => setText(e.target.value)}
            placeholder="192.168.1.200-192.168.1.220"
            className="font-mono"
          />
          <p className="text-muted-foreground text-xs">
            Uma ou mais faixas (início-fim), CIDRs ou IPs, separados por vírgula. Use endereços fora do DHCP do roteador e que nenhuma máquina use; eles
            precisam estar na mesma rede (L2) dos nós.
          </p>
        </div>
        <ConfirmButton
          variant="default"
          size="default"
          disabled={addresses.length === 0 || run.isPending || !!running}
          title={current || installed ? "Atualizar a faixa do MetalLB?" : "Instalar o MetalLB?"}
          description={
            <p>
              No primeiro control plane: aplica o manifesto oficial do MetalLB (namespace metallb-system), espera controller e speakers e grava o IPAddressPool{" "}
              <code>{addresses.join(", ")}</code> com anúncio L2. Services LoadBalancer existentes recebem IP em seguida.
            </p>
          }
          confirmLabel={current || installed ? "Atualizar" : "Instalar"}
          onConfirm={() => run.mutate()}
        >
          {running ? <Loader2 className="animate-spin" /> : <Network />} {current || installed ? "Atualizar faixa" : "Instalar MetalLB"}
        </ConfirmButton>
        <ErrorText error={run.error} />
        {(job ?? running ?? lastJob) && <JobLink job={running ?? lastJob ?? job!} />}
      </CardContent>
    </Card>
  );
}
