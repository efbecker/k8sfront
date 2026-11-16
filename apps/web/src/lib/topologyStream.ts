import type { K8sNode, K8sPod, K8sService } from "./api";

export type TopoKind = "node" | "pod" | "service";
export type TopoConnState = "connecting" | "live" | "error" | "reconnecting" | "closed";

export interface TopoMessage {
  type: "snapshot" | "upsert" | "delete" | "status";
  kind?: TopoKind;
  uid?: string;
  node?: K8sNode;
  pod?: K8sPod;
  service?: K8sService;
  nodes?: K8sNode[];
  pods?: K8sPod[];
  services?: K8sService[];
  state?: "connecting" | "live" | "error";
  message?: string;
  at: string;
}

/** A change worth animating / listing in the activity feed. */
export interface TopoChange {
  kind: TopoKind;
  uid: string;
  name: string;
  namespace?: string;
  action: "added" | "removed" | "status" | "restarted";
  detail?: string;
  at: number;
}

/** Short human status for a pod. */
export function podStatus(p: K8sPod): string {
  if (p.reason) return p.reason;
  if (p.phase === "Running" && p.ready < p.containers) return "NotReady";
  return p.phase;
}

export type PodHealth = "ok" | "pending" | "problem" | "done" | "terminating";

export function podHealth(p: K8sPod): PodHealth {
  if (p.reason === "Terminating") return "terminating";
  if (p.phase === "Succeeded") return "done";
  if (p.phase === "Failed") return "problem";
  if (p.reason && p.reason !== "Completed") return "problem";
  if (p.phase === "Pending") return "pending";
  if (p.phase === "Running" && p.ready < p.containers) return "pending";
  return p.phase === "Running" ? "ok" : "pending";
}

export function selects(svc: K8sService, pod: K8sPod) {
  if (svc.namespace !== pod.namespace || !svc.selector) return false;
  const entries = Object.entries(svc.selector);
  if (entries.length === 0) return false;
  return entries.every(([k, v]) => pod.labels?.[k] === v);
}

/**
 * Client-side mirror of the cluster topology. Applies snapshot and delta
 * messages and reports what changed so the view can animate it.
 */
export class TopologyModel {
  nodes = new Map<string, K8sNode>();
  pods = new Map<string, K8sPod>();
  services = new Map<string, K8sService>();
  version = 0;
  hasSnapshot = false;

  apply(msg: TopoMessage): TopoChange[] {
    const at = Date.now();
    const changes: TopoChange[] = [];
    switch (msg.type) {
      case "snapshot": {
        const prevPods = this.hasSnapshot ? this.pods : null;
        this.nodes = new Map((msg.nodes ?? []).map((n) => [n.uid, n]));
        this.pods = new Map((msg.pods ?? []).map((p) => [p.uid, p]));
        this.services = new Map((msg.services ?? []).map((s) => [s.uid, s]));
        // After a reconnect, diff against what we had so nothing pops silently.
        if (prevPods) {
          for (const [uid, p] of this.pods)
            if (!prevPods.has(uid)) changes.push({ kind: "pod", uid, name: p.name, namespace: p.namespace, action: "added", at });
          for (const [uid, p] of prevPods)
            if (!this.pods.has(uid)) changes.push({ kind: "pod", uid, name: p.name, namespace: p.namespace, action: "removed", at });
        }
        this.hasSnapshot = true;
        break;
      }
      case "upsert": {
        if (msg.kind === "pod" && msg.pod) {
          const old = this.pods.get(msg.pod.uid);
          const p = msg.pod;
          this.pods.set(p.uid, p);
          if (!old) changes.push({ kind: "pod", uid: p.uid, name: p.name, namespace: p.namespace, action: "added", detail: p.node, at });
          else if (p.restarts > old.restarts)
            changes.push({ kind: "pod", uid: p.uid, name: p.name, namespace: p.namespace, action: "restarted", detail: `${p.restarts} reinícios`, at });
          else if (podStatus(p) !== podStatus(old) || old.node !== p.node)
            changes.push({ kind: "pod", uid: p.uid, name: p.name, namespace: p.namespace, action: "status", detail: podStatus(p), at });
        } else if (msg.kind === "node" && msg.node) {
          const old = this.nodes.get(msg.node.uid);
          const n = msg.node;
          this.nodes.set(n.uid, n);
          if (!old) changes.push({ kind: "node", uid: n.uid, name: n.name, action: "added", at });
          else if (old.ready !== n.ready || old.unschedulable !== n.unschedulable)
            changes.push({ kind: "node", uid: n.uid, name: n.name, action: "status", detail: nodeStatus(n), at });
        } else if (msg.kind === "service" && msg.service) {
          const s = msg.service;
          if (!this.services.has(s.uid)) changes.push({ kind: "service", uid: s.uid, name: s.name, namespace: s.namespace, action: "added", at });
          this.services.set(s.uid, s);
        }
        break;
      }
      case "delete": {
        const uid = msg.uid ?? "";
        const map = msg.kind === "pod" ? this.pods : msg.kind === "node" ? this.nodes : this.services;
        const old = map.get(uid) as { name: string; namespace?: string } | undefined;
        if (old && msg.kind) {
          map.delete(uid);
          changes.push({ kind: msg.kind, uid, name: old.name, namespace: old.namespace, action: "removed", at });
        }
        break;
      }
      case "status":
        return changes;
    }
    this.version++;
    return changes;
  }
}

export function nodeStatus(n: K8sNode) {
  if (!n.ready) return "NotReady";
  if (n.unschedulable) return "Cordoned";
  return "Ready";
}

export interface TopologyStreamHandlers {
  onMessage: (msg: TopoMessage) => void;
  onState: (state: TopoConnState, message?: string) => void;
}

/** Follows /clusters/{id}/k8s/topology/stream, reconnecting with backoff. Every reconnect starts with a fresh snapshot. */
export class TopologyStream {
  private ws?: WebSocket;
  private attempt = 0;
  private timer?: ReturnType<typeof setTimeout>;
  private stopped = false;

  constructor(
    private clusterId: string,
    private handlers: TopologyStreamHandlers,
    private makeSocket: (url: string) => WebSocket = (url) => new WebSocket(url),
  ) {}

  start() {
    this.stopped = false;
    this.connect();
  }

  stop() {
    this.stopped = true;
    clearTimeout(this.timer);
    this.ws?.close();
    this.handlers.onState("closed");
  }

  handle(msg: TopoMessage) {
    if (msg.type === "status") {
      if (msg.state === "live") this.attempt = 0;
      this.handlers.onState(msg.state ?? "connecting", msg.message);
    }
    this.handlers.onMessage(msg);
  }

  private connect() {
    this.handlers.onState(this.attempt === 0 ? "connecting" : "reconnecting");
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    const ws = this.makeSocket(`${proto}//${location.host}/api/v1/clusters/${this.clusterId}/k8s/topology/stream`);
    this.ws = ws;
    ws.onmessage = (ev) => {
      try {
        this.handle(JSON.parse(ev.data as string) as TopoMessage);
      } catch {
        /* ignore malformed frames */
      }
    };
    ws.onclose = () => {
      if (this.stopped || this.ws !== ws) return;
      this.handlers.onState("reconnecting");
      const delay = Math.min(15000, 500 * 2 ** this.attempt) * (0.75 + Math.random() * 0.5);
      this.attempt++;
      this.timer = setTimeout(() => this.connect(), delay);
    };
  }
}
