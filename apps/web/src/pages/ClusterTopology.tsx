import { useEffect, useMemo, useRef, useState } from "react";
import { useParams } from "react-router-dom";
import { Box, Diamond, Maximize2, Minus, Plus, Search, Server, X } from "lucide-react";
import type { K8sPod } from "@/lib/api";
import { cn, formatBytes } from "@/lib/utils";
import {
  nodeStatus,
  podHealth,
  podStatus,
  selects,
  TopologyModel,
  TopologyStream,
  type TopoChange,
  type TopoConnState,
} from "@/lib/topologyStream";
import { SYSTEM_NAMESPACES, TopologyScene, type ColorMode, type Hit, type SceneOptions } from "@/components/topology/scene";
import { ClusterHeader } from "@/components/cluster/ClusterHeader";
import { age, NodeBadge, PodBadge } from "@/components/cluster/k8s";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input, NativeSelect } from "@/components/ui/input";

const connLabel: Record<TopoConnState, { label: string; cls: string }> = {
  connecting: { label: "Conectando…", cls: "bg-warning" },
  reconnecting: { label: "Reconectando…", cls: "bg-warning" },
  live: { label: "Ao vivo", cls: "bg-success" },
  error: { label: "API instável", cls: "bg-destructive" },
  closed: { label: "Desconectado", cls: "bg-muted-foreground" },
};


function Toggle({ checked, onChange, children }: { checked: boolean; onChange: (v: boolean) => void; children: React.ReactNode }) {
  return (
    <label className="flex cursor-pointer items-center gap-1.5 text-sm select-none">
      <input type="checkbox" className="accent-primary size-3.5" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      {children}
    </label>
  );
}

export function ClusterTopologyPage() {
  const { id = "" } = useParams();
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const sceneRef = useRef<TopologyScene | null>(null);
  const modelRef = useRef(new TopologyModel());
  const [version, setVersion] = useState(0);
  const [conn, setConn] = useState<{ state: TopoConnState; message?: string }>({ state: "connecting" });
  const [feed, setFeed] = useState<TopoChange[]>([]);
  const [selected, setSelected] = useState<Hit | null>(null);
  const [hover, setHover] = useState<{ hit: Hit; x: number; y: number } | null>(null);
  const [opts, setOpts] = useState<SceneOptions>({
    hideSystem: false,
    showServices: true,
    traffic: true,
    colorBy: "status",
    namespace: "",
    search: "",
  });
  const optsRef = useRef(opts);
  optsRef.current = opts;

  // Scene + stream lifecycle.
  useEffect(() => {
    const model = new TopologyModel();
    modelRef.current = model;
    const scene = new TopologyScene(canvasRef.current!, model, optsRef.current);
    sceneRef.current = scene;
    scene.onSelect = setSelected;
    scene.onHover = (hit, x, y) => setHover(hit ? { hit, x, y } : null);
    let pending = false;
    const stream = new TopologyStream(id, {
      onMessage: (msg) => {
        const changes = model.apply(msg);
        if (msg.type === "status") return;
        scene.sync(changes);
        const notable = changes.filter((c) => !(msg.type === "snapshot" && c.action === "added" && changes.length > 30));
        if (notable.length) setFeed((f) => [...notable.reverse(), ...f].slice(0, 40));
        // Throttle React updates (side panel, counters) to animation frames.
        if (!pending) {
          pending = true;
          requestAnimationFrame(() => {
            pending = false;
            setVersion(model.version);
          });
        }
      },
      onState: (state, message) => setConn({ state, message }),
    });
    stream.start();
    return () => {
      stream.stop();
      scene.destroy();
      sceneRef.current = null;
    };
  }, [id]);

  useEffect(() => {
    sceneRef.current?.setOptions(opts);
  }, [opts]);
  useEffect(() => {
    sceneRef.current?.setSelected(selected);
  }, [selected]);

  const model = modelRef.current;
  const stats = useMemo(() => {
    const pods = [...model.pods.values()];
    return {
      nodes: model.nodes.size,
      nodesReady: [...model.nodes.values()].filter((n) => n.ready).length,
      pods: pods.length,
      running: pods.filter((p) => podHealth(p) === "ok").length,
      pending: pods.filter((p) => podHealth(p) === "pending").length,
      problem: pods.filter((p) => podHealth(p) === "problem").length,
      services: model.services.size,
      namespaces: [...new Set(pods.map((p) => p.namespace))].sort(),
    };
  }, [version, model]);

  const set = (patch: Partial<SceneOptions>) => setOpts((o) => ({ ...o, ...patch }));
  const select = (hit: Hit | null) => setSelected(hit);
  const live = conn.state === "live" && model.hasSnapshot;

  return (
    <>
      <ClusterHeader
        id={id}
        right={
          <span className="flex items-center gap-2 rounded-full border px-3 py-1 text-xs" title={conn.message}>
            <span className={cn("relative flex size-2")}>
              {live && <span className="bg-success absolute inline-flex size-full animate-ping rounded-full opacity-60" />}
              <span className={cn("relative inline-flex size-2 rounded-full", connLabel[conn.state].cls)} />
            </span>
            {connLabel[conn.state].label}
          </span>
        }
      />

      {conn.state === "error" && conn.message && (
        <div className="border-destructive/40 bg-destructive/10 text-destructive mb-3 rounded-lg border px-3 py-2 text-sm">{conn.message}</div>
      )}

      <div className="mb-3 grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-6">
        <Stat label="Nós prontos" value={`${stats.nodesReady}/${stats.nodes}`} />
        <Stat label="Pods" value={stats.pods} />
        <Stat label="Rodando" value={stats.running} dot="bg-success" />
        <Stat label="Pendentes" value={stats.pending} dot="bg-warning" />
        <Stat label="Com problema" value={stats.problem} dot="bg-destructive" />
        <Stat label="Services" value={stats.services} />
      </div>

      <div className="bg-card mb-3 flex flex-wrap items-center gap-x-4 gap-y-2 rounded-lg border px-3 py-2">
        <NativeSelect className="h-8 w-56" value={opts.namespace} onChange={(e) => set({ namespace: e.target.value })} aria-label="Namespace">
          <option value="">Todos os namespaces</option>
          {stats.namespaces.map((ns) => (
            <option key={ns} value={ns}>
              {ns}
            </option>
          ))}
        </NativeSelect>
        <div className="relative">
          <Search className="text-muted-foreground absolute top-2 left-2 size-4" />
          <Input className="h-8 w-48 pl-7" placeholder="Buscar pod ou service" value={opts.search} onChange={(e) => set({ search: e.target.value })} />
        </div>
        <Toggle checked={opts.hideSystem} onChange={(v) => set({ hideSystem: v })}>
          Ocultar namespaces do sistema
        </Toggle>
        <Toggle checked={opts.showServices} onChange={(v) => set({ showServices: v })}>
          Services
        </Toggle>
        <Toggle checked={opts.traffic} onChange={(v) => set({ traffic: v })}>
          Animar tráfego
        </Toggle>
        <div className="flex items-center gap-1 text-sm">
          <span className="text-muted-foreground">Cor:</span>
          {(["status", "namespace"] as ColorMode[]).map((m) => (
            <button
              key={m}
              onClick={() => set({ colorBy: m })}
              className={cn("rounded-md px-2 py-0.5", opts.colorBy === m ? "bg-primary/15 text-primary font-medium" : "hover:bg-accent")}
            >
              {m === "status" ? "Status" : "Namespace"}
            </button>
          ))}
        </div>
      </div>

      <div className="flex flex-col gap-3 lg:flex-row">
        <div className="bg-background relative h-[62vh] min-h-[420px] flex-1 overflow-hidden rounded-xl border">
          <canvas ref={canvasRef} className="block cursor-grab touch-none" aria-label="Topologia do cluster" />
          {!model.hasSnapshot && (
            <div className="text-muted-foreground absolute inset-0 flex items-center justify-center text-sm">
              {conn.state === "error" ? "Sem conexão com a API do Kubernetes." : "Carregando a topologia do cluster…"}
            </div>
          )}
          <div className="absolute top-3 right-3 flex flex-col gap-1">
            <Button size="icon" variant="outline" className="size-8" title="Aproximar" onClick={() => sceneRef.current?.zoom(1.25)}>
              <Plus />
            </Button>
            <Button size="icon" variant="outline" className="size-8" title="Afastar" onClick={() => sceneRef.current?.zoom(0.8)}>
              <Minus />
            </Button>
            <Button size="icon" variant="outline" className="size-8" title="Enquadrar tudo" onClick={() => sceneRef.current?.fit()}>
              <Maximize2 />
            </Button>
          </div>
          <ActivityFeed feed={feed} onPick={(c) => model[c.kind === "pod" ? "pods" : c.kind === "node" ? "nodes" : "services"].has(c.uid) && select({ kind: c.kind, uid: c.uid })} />
          {hover && <Tooltip model={model} hit={hover.hit} x={hover.x} y={hover.y} />}
          <div className="text-muted-foreground pointer-events-none absolute right-3 bottom-2 text-[11px]">
            Arraste para mover · role para zoom · arraste um nó para reposicionar
          </div>
        </div>
        <aside className="bg-card w-full shrink-0 overflow-y-auto rounded-xl border p-4 lg:h-[62vh] lg:min-h-[420px] lg:w-80">
          {selected ? (
            <Details model={model} hit={selected} onSelect={select} version={version} />
          ) : (
            <Legend colorBy={opts.colorBy} namespaces={stats.namespaces} scene={sceneRef.current} />
          )}
        </aside>
      </div>
    </>
  );
}

function Stat({ label, value, dot }: { label: string; value: React.ReactNode; dot?: string }) {
  return (
    <div className="bg-card rounded-lg border px-3 py-2">
      <div className="text-muted-foreground flex items-center gap-1.5 text-xs">
        {dot && <span className={cn("size-2 rounded-full", dot)} />}
        {label}
      </div>
      <div className="text-xl font-semibold tabular-nums">{value}</div>
    </div>
  );
}

const actionText: Record<TopoChange["action"], string> = {
  added: "criado",
  removed: "removido",
  status: "→",
  restarted: "reiniciou",
};

function ActivityFeed({ feed, onPick }: { feed: TopoChange[]; onPick: (c: TopoChange) => void }) {
  if (!feed.length) return null;
  return (
    <div className="bg-card/85 absolute bottom-7 left-3 max-h-40 w-80 max-w-[calc(100%-1.5rem)] overflow-hidden rounded-lg border text-xs shadow-sm backdrop-blur">
      <div className="text-muted-foreground border-b px-2.5 py-1.5 font-medium">Atividade ao vivo</div>
      <ul className="max-h-32 overflow-y-auto">
        {feed.slice(0, 12).map((c, i) => (
          <li key={`${c.uid}-${c.at}-${i}`}>
            <button className="hover:bg-accent flex w-full items-center gap-2 px-2.5 py-1 text-left" onClick={() => onPick(c)}>
              <span
                className={cn(
                  "size-1.5 shrink-0 rounded-full",
                  c.action === "removed" ? "bg-muted-foreground" : c.action === "restarted" ? "bg-destructive" : c.action === "added" ? "bg-success" : "bg-primary",
                )}
              />
              <span className="text-muted-foreground shrink-0 tabular-nums">{new Date(c.at).toLocaleTimeString("pt-BR")}</span>
              <span className="truncate">
                {c.kind === "node" ? "Nó" : c.kind === "service" ? "Service" : "Pod"} <b className="font-medium">{c.name}</b> {actionText[c.action]}{" "}
                {c.detail && <span className="text-muted-foreground">{c.action === "added" && c.kind === "pod" ? `em ${c.detail}` : c.detail}</span>}
              </span>
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}

function Tooltip({ model, hit, x, y }: { model: TopologyModel; hit: Hit; x: number; y: number }) {
  let title = "";
  let sub = "";
  if (hit.kind === "pod") {
    const p = model.pods.get(hit.uid);
    if (!p) return null;
    title = p.name;
    sub = `${p.namespace} · ${podStatus(p)} · ${p.ready}/${p.containers} prontos`;
  } else if (hit.kind === "node") {
    const n = model.nodes.get(hit.uid);
    if (!n) return null;
    title = n.name;
    sub = `${nodeStatus(n)} · ${n.internalIP} · ${n.kubeletVersion}`;
  } else {
    const s = model.services.get(hit.uid);
    if (!s) return null;
    title = s.name;
    sub = `${s.namespace} · ${s.type} · ${s.clusterIP}`;
  }
  return (
    <div className="bg-popover pointer-events-none absolute z-10 max-w-72 rounded-md border px-2.5 py-1.5 text-xs shadow-md" style={{ left: x + 14, top: y + 14 }}>
      <div className="truncate font-medium">{title}</div>
      <div className="text-muted-foreground">{sub}</div>
    </div>
  );
}

function Legend({ colorBy, namespaces, scene }: { colorBy: ColorMode; namespaces: string[]; scene: TopologyScene | null }) {
  return (
    <div className="space-y-5 text-sm">
      <p className="text-muted-foreground">Clique em um elemento para ver detalhes. Tudo aqui é atualizado em tempo real pela API do Kubernetes.</p>
      <LegendItem icon={<Server className="size-4" />} title="Nó">
        Servidor Debian que roda o kubelet. A borda mostra o estado: <b className="text-success">pronto</b>,{" "}
        <b className="text-warning">cordoned</b> ou <b className="text-destructive">NotReady</b>.
      </LegendItem>
      <LegendItem icon={<Box className="size-4" />} title="Pod">
        Um ou mais containers agendados num nó. Os pods orbitam o nó onde estão rodando e animam ao nascer, mudar de estado ou sair.
      </LegendItem>
      <LegendItem icon={<Diamond className="size-4" />} title="Service">
        Endereço estável que balanceia tráfego entre os pods que casam com o seletor. As partículas indicam o caminho do tráfego.
      </LegendItem>
      {colorBy === "status" ? (
        <div className="space-y-1.5">
          <div className="font-medium">Cores dos pods</div>
          {[
            ["bg-success", "Rodando e pronto"],
            ["bg-warning", "Pendente ou iniciando (anel tracejado)"],
            ["bg-destructive", "Com problema (CrashLoop, ImagePull, falha)"],
            ["bg-muted-foreground", "Concluído ou encerrando"],
          ].map(([c, l]) => (
            <div key={l} className="flex items-center gap-2">
              <span className={cn("size-3 rounded-full", c)} />
              {l}
            </div>
          ))}
        </div>
      ) : (
        <div className="space-y-1.5">
          <div className="font-medium">Namespaces</div>
          {namespaces.map((ns) => (
            <div key={ns} className="flex items-center gap-2">
              <span className="size-3 rounded-full" style={{ background: scene?.namespaceColor(ns) }} />
              {ns}
              {SYSTEM_NAMESPACES.has(ns) && <span className="text-muted-foreground text-xs">(sistema)</span>}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function LegendItem({ icon, title, children }: { icon: React.ReactNode; title: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="mb-1 flex items-center gap-2 font-medium">
        <span className="bg-primary/10 text-primary flex size-7 items-center justify-center rounded-full">{icon}</span>
        {title}
      </div>
      <p className="text-muted-foreground">{children}</p>
    </div>
  );
}

function Row({ k, children }: { k: string; children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[7rem_1fr] gap-2 py-1 text-sm">
      <div className="text-muted-foreground">{k}</div>
      <div className="min-w-0 break-words">{children}</div>
    </div>
  );
}

function Labels({ labels }: { labels?: Record<string, string> }) {
  const entries = Object.entries(labels ?? {});
  if (!entries.length) return <span className="text-muted-foreground">—</span>;
  return (
    <div className="flex flex-wrap gap-1">
      {entries.map(([k, v]) => (
        <Badge key={k} variant="secondary" className="max-w-full font-mono text-[10px] break-all whitespace-normal">
          {k}={v}
        </Badge>
      ))}
    </div>
  );
}

/** `version` only forces a re-render when the live model changes. */
function Details({ model, hit, onSelect }: { model: TopologyModel; hit: Hit; onSelect: (h: Hit | null) => void; version: number }) {
  const close = (
    <Button variant="ghost" size="icon" className="size-7" onClick={() => onSelect(null)} aria-label="Fechar">
      <X />
    </Button>
  );
  if (hit.kind === "node") {
    const n = model.nodes.get(hit.uid);
    if (!n) return <Gone close={close} />;
    const pods = [...model.pods.values()].filter((p) => p.node === n.name);
    return (
      <DetailShell icon={<Server className="size-4" />} kind="Nó" name={n.name} close={close}>
        <Row k="Estado">
          <NodeBadge n={n} />
        </Row>
        <Row k="Papéis">{n.roles?.join(", ") || "worker"}</Row>
        <Row k="IP interno">
          <span className="font-mono text-xs">{n.internalIP}</span>
        </Row>
        <Row k="Kubelet">{n.kubeletVersion}</Row>
        <Row k="Sistema">{n.osImage}</Row>
        <Row k="Kernel">{n.kernelVersion}</Row>
        <Row k="Runtime">{n.containerRuntime}</Row>
        <Row k="CPU / Memória">
          {(n.cpuMillis / 1000).toFixed(1)} vCPU · {formatBytes(n.memoryBytes)}
        </Row>
        <Row k="Pods">
          {pods.length} / {n.podsCapacity}
        </Row>
        {n.podCIDR && (
          <Row k="Pod CIDR">
            <span className="font-mono text-xs">{n.podCIDR}</span>
          </Row>
        )}
        {!!n.taints?.length && (
          <Row k="Taints">
            {n.taints.map((t) => (
              <div key={t} className="font-mono text-xs">
                {t}
              </div>
            ))}
          </Row>
        )}
        {!!n.conditions?.length && <Row k="Alertas">{n.conditions.join(", ")}</Row>}
        <Row k="Idade">{age(n.created)}</Row>
        <PodList pods={pods} onSelect={onSelect} />
      </DetailShell>
    );
  }
  if (hit.kind === "pod") {
    const p = model.pods.get(hit.uid);
    if (!p) return <Gone close={close} />;
    const node = [...model.nodes.values()].find((n) => n.name === p.node);
    const svcs = [...model.services.values()].filter((s) => selects(s, p));
    return (
      <DetailShell icon={<Box className="size-4" />} kind="Pod" name={p.name} close={close}>
        <Row k="Estado">
          <PodBadge p={p} />
        </Row>
        <Row k="Namespace">{p.namespace}</Row>
        <Row k="Nó">
          {node ? (
            <button className="text-primary hover:underline" onClick={() => onSelect({ kind: "node", uid: node.uid })}>
              {node.name}
            </button>
          ) : (
            <span className="text-warning">não agendado</span>
          )}
        </Row>
        <Row k="IP">
          <span className="font-mono text-xs">{p.ip || "—"}</span>
        </Row>
        <Row k="Containers">
          {p.ready}/{p.containers} prontos · {p.restarts} reinícios
        </Row>
        {p.ownerKind && (
          <Row k="Controlador">
            {p.ownerKind}/{p.ownerName}
          </Row>
        )}
        <Row k="Imagens">
          {(p.images ?? []).map((i) => (
            <div key={i} className="font-mono text-xs break-all">
              {i}
            </div>
          ))}
        </Row>
        {p.qosClass && <Row k="QoS">{p.qosClass}</Row>}
        <Row k="Idade">{age(p.created)}</Row>
        <Row k="Services">
          {svcs.length ? (
            svcs.map((s) => (
              <button key={s.uid} className="text-primary block hover:underline" onClick={() => onSelect({ kind: "service", uid: s.uid })}>
                {s.name}
              </button>
            ))
          ) : (
            <span className="text-muted-foreground">nenhum</span>
          )}
        </Row>
        <Row k="Labels">
          <Labels labels={p.labels} />
        </Row>
      </DetailShell>
    );
  }
  const s = model.services.get(hit.uid);
  if (!s) return <Gone close={close} />;
  const pods = [...model.pods.values()].filter((p) => selects(s, p));
  return (
    <DetailShell icon={<Diamond className="size-4" />} kind="Service" name={s.name} close={close}>
      <Row k="Namespace">{s.namespace}</Row>
      <Row k="Tipo">{s.type}</Row>
      <Row k="Cluster IP">
        <span className="font-mono text-xs">{s.clusterIP || "—"}</span>
      </Row>
      {!!s.externalIPs?.length && (
        <Row k="IP externo">
          <span className="font-mono text-xs">{s.externalIPs.join(", ")}</span>
        </Row>
      )}
      <Row k="Portas">
        <span className="font-mono text-xs">{s.ports.join(", ") || "—"}</span>
      </Row>
      <Row k="Seletor">
        <Labels labels={s.selector} />
      </Row>
      <Row k="Idade">{age(s.created)}</Row>
      <PodList pods={pods} onSelect={onSelect} />
    </DetailShell>
  );
}

function DetailShell({ icon, kind, name, close, children }: { icon: React.ReactNode; kind: string; name: string; close: React.ReactNode; children: React.ReactNode }) {
  return (
    <div>
      <div className="mb-3 flex items-start gap-2">
        <span className="bg-primary/10 text-primary flex size-8 shrink-0 items-center justify-center rounded-full">{icon}</span>
        <div className="min-w-0 flex-1">
          <div className="text-muted-foreground text-xs">{kind}</div>
          <div className="font-semibold break-all">{name}</div>
        </div>
        {close}
      </div>
      <div className="divide-y">{children}</div>
    </div>
  );
}

function Gone({ close }: { close: React.ReactNode }) {
  return (
    <div className="flex items-start justify-between gap-2 text-sm">
      <p className="text-muted-foreground">Este objeto não existe mais no cluster.</p>
      {close}
    </div>
  );
}

function PodList({ pods, onSelect }: { pods: K8sPod[]; onSelect: (h: Hit) => void }) {
  return (
    <div className="pt-3">
      <div className="text-muted-foreground mb-1 text-xs">Pods ({pods.length})</div>
      <ul className="space-y-0.5">
        {pods
          .sort((a, b) => a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name))
          .map((p) => (
            <li key={p.uid}>
              <button className="hover:bg-accent flex w-full items-center gap-2 rounded px-1 py-0.5 text-left text-xs" onClick={() => onSelect({ kind: "pod", uid: p.uid })}>
                <HealthDot p={p} />
                <span className="truncate">{p.name}</span>
                <span className="text-muted-foreground ml-auto shrink-0">{p.namespace}</span>
              </button>
            </li>
          ))}
      </ul>
    </div>
  );
}

function HealthDot({ p }: { p: K8sPod }) {
  const h = podHealth(p);
  return (
    <span
      className={cn(
        "size-2 shrink-0 rounded-full",
        h === "ok" ? "bg-success" : h === "pending" ? "bg-warning" : h === "problem" ? "bg-destructive" : "bg-muted-foreground",
      )}
    />
  );
}

