import type { K8sPod, K8sService } from "@/lib/api";
import { podHealth, selects, type TopoChange, type TopologyModel } from "@/lib/topologyStream";

/**
 * Canvas renderer for the live cluster topology. Nodes sit on a ring, each
 * with its pods orbiting it in concentric slots; services float between the
 * pods they select. Positions ease toward layout targets every frame, so
 * scheduling, deletions and rescheduling are animated rather than jumping.
 */

export type ColorMode = "status" | "namespace";

export interface SceneOptions {
  hideSystem: boolean;
  showServices: boolean;
  traffic: boolean;
  colorBy: ColorMode;
  namespace: string; // "" = all
  search: string;
}

export interface Hit {
  kind: "node" | "pod" | "service";
  uid: string;
}

interface Sprite {
  kind: "node" | "pod" | "service";
  uid: string;
  x: number;
  y: number;
  tx: number;
  ty: number;
  scale: number;
  removing: number; // timestamp when removal started, 0 = alive
  pulse: number; // timestamp of the last pulse
  pulseKind: "info" | "bad";
  pinned?: boolean; // node dragged by the user
  orbit?: number; // node: radius of its pod cloud
}

interface Palette {
  bg: string;
  card: string;
  fg: string;
  muted: string;
  border: string;
  primary: string;
  success: string;
  warning: string;
  destructive: string;
  dark: boolean;
}

// Categorical slots (fixed order, never cycled); a 9th namespace folds into gray.
const NS_LIGHT = ["#2a78d6", "#eb6834", "#1baf7a", "#eda100", "#e87ba4", "#008300", "#6250d6", "#e34948"];
const NS_DARK = ["#3987e5", "#d95926", "#199e70", "#c98500", "#d55181", "#0ca30c", "#9085e9", "#e66767"];

export const SYSTEM_NAMESPACES = new Set(["kube-system", "kube-public", "kube-node-lease", "calico-system", "calico-apiserver", "tigera-operator"]);

const POD_R = 9;
const NODE_HALF = 24;
const SLOT = 25;
const RING0 = 66;
const RING_STEP = 25;

function hash(s: string) {
  let h = 2166136261;
  for (let i = 0; i < s.length; i++) h = Math.imul(h ^ s.charCodeAt(i), 16777619);
  return (h >>> 0) / 4294967295;
}

function readPalette(): Palette {
  const cs = getComputedStyle(document.documentElement);
  const v = (n: string) => cs.getPropertyValue(n).trim() || "#888";
  return {
    bg: v("--background"),
    card: v("--card"),
    fg: v("--foreground"),
    muted: v("--muted-foreground"),
    border: v("--border"),
    primary: v("--primary"),
    success: v("--success"),
    warning: v("--warning"),
    destructive: v("--destructive"),
    dark: document.documentElement.classList.contains("dark"),
  };
}

export class TopologyScene {
  private ctx: CanvasRenderingContext2D;
  private sprites = new Map<string, Sprite>();
  private cam = { x: 0, y: 0, k: 1 };
  private userMovedCamera = false;
  private raf = 0;
  private last = performance.now();
  private pal: Palette = readPalette();
  private nsSlots = new Map<string, number>();
  private width = 0;
  private height = 0;
  private dpr = 1;
  private ro: ResizeObserver;
  private mo: MutationObserver;
  private drag: { mode: "pan" | "node"; uid?: string; sx: number; sy: number; moved: boolean; ox: number; oy: number } | null = null;
  private hover: Hit | null = null;
  private selected: Hit | null = null;
  private related = new Set<string>();
  private visiblePods: K8sPod[] = [];
  private visibleServices: K8sService[] = [];
  private svcEdges: [string, string][] = [];
  /** Selectorless services whose endpoints are node IPs (kube-apiserver on control planes). */
  private svcNodeEdges: [string, string][] = [];
  private unscheduledAnchor = { x: 0, y: 0 };
  private hasUnscheduled = false;
  private nodeUidByName = new Map<string, string>();

  onHover: (hit: Hit | null, x: number, y: number) => void = () => {};
  onSelect: (hit: Hit | null) => void = () => {};

  constructor(
    private canvas: HTMLCanvasElement,
    private model: TopologyModel,
    private opts: SceneOptions,
  ) {
    this.ctx = canvas.getContext("2d")!;
    this.ro = new ResizeObserver(() => this.resize());
    this.ro.observe(canvas.parentElement ?? canvas);
    this.mo = new MutationObserver(() => (this.pal = readPalette()));
    this.mo.observe(document.documentElement, { attributes: true, attributeFilter: ["class", "style"] });
    canvas.addEventListener("pointerdown", this.onDown);
    window.addEventListener("pointermove", this.onMove);
    window.addEventListener("pointerup", this.onUp);
    canvas.addEventListener("pointerleave", this.onLeave);
    canvas.addEventListener("wheel", this.onWheel, { passive: false });
    this.resize();
    this.raf = requestAnimationFrame(this.frame);
  }

  destroy() {
    cancelAnimationFrame(this.raf);
    this.ro.disconnect();
    this.mo.disconnect();
    this.canvas.removeEventListener("pointerdown", this.onDown);
    window.removeEventListener("pointermove", this.onMove);
    window.removeEventListener("pointerup", this.onUp);
    this.canvas.removeEventListener("pointerleave", this.onLeave);
    this.canvas.removeEventListener("wheel", this.onWheel);
  }

  setOptions(o: SceneOptions) {
    this.opts = o;
    this.sync([]);
  }

  setSelected(hit: Hit | null) {
    this.selected = hit;
    this.computeRelated();
  }

  namespaceColor(ns: string): string {
    if (!this.nsSlots.has(ns)) this.nsSlots.set(ns, this.nsSlots.size);
    const slot = this.nsSlots.get(ns)!;
    const list = this.pal.dark ? NS_DARK : NS_LIGHT;
    return slot < list.length ? list[slot]! : this.pal.muted;
  }

  /** Recomputes the layout after the model changed; `changes` drive pulses. */
  sync(changes: TopoChange[]) {
    const now = performance.now();
    const m = this.model;
    // Stable namespace colors: first come, first served, over all namespaces.
    for (const ns of [...new Set([...m.pods.values()].map((p) => p.namespace))].sort()) this.namespaceColor(ns);

    const o = this.opts;
    const podVisible = (p: K8sPod) => (!o.namespace || p.namespace === o.namespace) && !(o.hideSystem && SYSTEM_NAMESPACES.has(p.namespace));
    this.visiblePods = [...m.pods.values()].filter(podVisible);
    this.visibleServices = o.showServices
      ? [...m.services.values()].filter((s) => (!o.namespace || s.namespace === o.namespace) && !(o.hideSystem && SYSTEM_NAMESPACES.has(s.namespace)))
      : [];

    const nodes = [...m.nodes.values()].sort((a, b) => {
      const ca = a.roles?.includes("control-plane") ? 0 : 1;
      const cb = b.roles?.includes("control-plane") ? 0 : 1;
      return ca - cb || a.name.localeCompare(b.name);
    });
    this.nodeUidByName = new Map(nodes.map((n) => [n.name, n.uid]));
    const byNode = new Map<string, K8sPod[]>();
    for (const p of this.visiblePods) {
      const key = this.nodeUidByName.has(p.node) ? p.node : "";
      if (!byNode.has(key)) byNode.set(key, []);
      byNode.get(key)!.push(p);
    }
    for (const list of byNode.values()) list.sort((a, b) => a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name));

    // Orbit radius per node from its pod count.
    const orbitFor = (count: number) => {
      let r = RING0;
      let left = count;
      while (left > 0) {
        const cap = Math.max(6, Math.floor((2 * Math.PI * r) / SLOT));
        left -= cap;
        if (left > 0) r += RING_STEP;
      }
      return r + 22;
    };
    const orbits = nodes.map((n) => orbitFor(byNode.get(n.name)?.length ?? 0));
    const circumference = orbits.reduce((acc, r) => acc + 2 * r + 90, 0);
    const R = nodes.length <= 1 ? 0 : Math.max(240, circumference / (2 * Math.PI));

    const alive = new Set<string>();
    const ensure = (kind: Sprite["kind"], uid: string, x: number, y: number, sx: number, sy: number) => {
      alive.add(uid);
      let s = this.sprites.get(uid);
      if (!s || s.removing) {
        s = { kind, uid, x: sx, y: sy, tx: x, ty: y, scale: 0, removing: 0, pulse: s ? 0 : now, pulseKind: "info" };
        this.sprites.set(uid, s);
      }
      if (!s.pinned) {
        s.tx = x;
        s.ty = y;
      }
      return s;
    };

    nodes.forEach((n, i) => {
      const a = -Math.PI / 2 + (i * 2 * Math.PI) / Math.max(1, nodes.length);
      const s = ensure("node", n.uid, Math.cos(a) * R, Math.sin(a) * R, Math.cos(a) * R, Math.sin(a) * R);
      s.orbit = orbits[i];
      const pods = byNode.get(n.name) ?? [];
      this.placeRing(pods, s.pinned ? s.x : s.tx, s.pinned ? s.y : s.ty, s, ensure);
    });

    // Unscheduled pods queue above the cluster.
    const pending = byNode.get("") ?? [];
    this.hasUnscheduled = pending.length > 0;
    if (pending.length) {
      const top = nodes.length <= 1 ? -(orbits[0] ?? 80) - 110 : -R - Math.max(...orbits, 80) - 110;
      this.unscheduledAnchor = { x: 0, y: top };
      const cols = Math.min(pending.length, 16);
      pending.forEach((p, i) => {
        const x = (i % cols) * SLOT - ((cols - 1) * SLOT) / 2;
        const y = top + Math.floor(i / cols) * SLOT;
        ensure("pod", p.uid, x, y, x, y - 40);
      });
    }

    // Services: centroid of their pods, nudged toward the middle, then relaxed.
    this.svcEdges = [];
    this.svcNodeEdges = [];
    const nodeByIP = new Map(nodes.filter((n) => n.internalIP).map((n) => [n.internalIP, n]));
    const podByIP = new Map(this.visiblePods.filter((p) => p.ip).map((p) => [p.ip!, p]));
    const svcSprites: Sprite[] = [];
    const nodeSprites = nodes.map((n) => this.sprites.get(n.uid)!);
    for (const svc of this.visibleServices) {
      let cx = 0;
      let cy = 0;
      let count = 0;
      for (const p of this.visiblePods) {
        if (!selects(svc, p)) continue;
        this.svcEdges.push([svc.uid, p.uid]);
        const ps = this.sprites.get(p.uid);
        if (ps) {
          cx += ps.tx;
          cy += ps.ty;
          count++;
        }
      }
      // No selector: follow the EndpointSlice IPs to nodes (or pods) instead.
      if (!Object.keys(svc.selector ?? {}).length) {
        for (const ip of svc.endpointIPs ?? []) {
          const n = nodeByIP.get(ip);
          const p = n ? undefined : podByIP.get(ip);
          const target = n ? this.sprites.get(n.uid) : p ? this.sprites.get(p.uid) : undefined;
          if (n) this.svcNodeEdges.push([svc.uid, n.uid]);
          else if (p) this.svcEdges.push([svc.uid, p.uid]);
          if (target) {
            cx += target.tx;
            cy += target.ty;
            count++;
          }
        }
      }
      let tx: number;
      let ty: number;
      if (count) {
        tx = (cx / count) * 0.55;
        ty = (cy / count) * 0.55;
      } else {
        const a = hash(svc.uid) * Math.PI * 2;
        const rr = R + Math.max(...orbits, 80) + 90;
        tx = Math.cos(a) * rr;
        ty = Math.sin(a) * rr;
      }
      tx += (hash(svc.uid + "x") - 0.5) * 30;
      ty += (hash(svc.uid + "y") - 0.5) * 30;
      const s = ensure("service", svc.uid, tx, ty, tx, ty);
      svcSprites.push(s);
    }
    for (let iter = 0; iter < 40; iter++) {
      for (let i = 0; i < svcSprites.length; i++) {
        const a = svcSprites[i]!;
        for (let j = i + 1; j < svcSprites.length; j++) {
          const b = svcSprites[j]!;
          let dx = b.tx - a.tx;
          let dy = b.ty - a.ty;
          let d = Math.hypot(dx, dy);
          if (d < 1) {
            dx = hash(a.uid + b.uid) - 0.5;
            dy = 0.5 - hash(b.uid + a.uid);
            d = Math.hypot(dx, dy) || 1;
          }
          if (d < 58) {
            const push = (58 - d) / 2;
            a.tx -= (dx / d) * push;
            a.ty -= (dy / d) * push;
            b.tx += (dx / d) * push;
            b.ty += (dy / d) * push;
          }
        }
        for (const n of nodeSprites) {
          const min = (n.orbit ?? RING0) + 26;
          const dx = a.tx - n.tx;
          const dy = a.ty - n.ty;
          const d = Math.hypot(dx, dy) || 1;
          if (d < min) {
            a.tx = n.tx + (dx / d) * min;
            a.ty = n.ty + (dy / d) * min;
          }
        }
      }
    }

    // Everything not in the layout anymore fades out.
    for (const s of this.sprites.values()) if (!alive.has(s.uid) && !s.removing) s.removing = now;

    for (const c of changes) {
      const s = this.sprites.get(c.uid);
      if (!s) continue;
      if (c.action === "status" || c.action === "restarted") {
        s.pulse = now;
        const p = c.kind === "pod" ? m.pods.get(c.uid) : undefined;
        const n = c.kind === "node" ? m.nodes.get(c.uid) : undefined;
        s.pulseKind = c.action === "restarted" || (p && podHealth(p) === "problem") || (n && !n.ready) ? "bad" : "info";
      }
    }
    this.computeRelated();
    if (!this.userMovedCamera) this.fit(false);
  }

  private placeRing(
    pods: K8sPod[],
    cx: number,
    cy: number,
    node: Sprite,
    ensure: (kind: Sprite["kind"], uid: string, x: number, y: number, sx: number, sy: number) => Sprite,
  ) {
    let r = RING0;
    let i = 0;
    let offsetA = hash(node.uid) * Math.PI * 2;
    while (i < pods.length) {
      const cap = Math.max(6, Math.floor((2 * Math.PI * r) / SLOT));
      const n = Math.min(cap, pods.length - i);
      // The last (possibly partial) ring is spread evenly instead of bunched.
      const step = (2 * Math.PI) / (n === pods.length - i ? n : cap);
      for (let j = 0; j < n; j++, i++) {
        const a = offsetA + j * step;
        ensure("pod", pods[i]!.uid, cx + Math.cos(a) * r, cy + Math.sin(a) * r, node.x, node.y);
      }
      r += RING_STEP;
      offsetA += step / 2;
    }
  }

  private computeRelated() {
    const rel = new Set<string>();
    const sel = this.selected;
    if (sel) {
      rel.add(sel.uid);
      const m = this.model;
      if (sel.kind === "node") {
        const n = m.nodes.get(sel.uid);
        for (const p of this.visiblePods) if (n && p.node === n.name) rel.add(p.uid);
        for (const [su, nu] of this.svcNodeEdges) if (nu === sel.uid) rel.add(su);
      } else if (sel.kind === "pod") {
        const p = m.pods.get(sel.uid);
        for (const n of m.nodes.values()) if (p && n.name === p.node) rel.add(n.uid);
        for (const [s, pu] of this.svcEdges) if (pu === sel.uid) rel.add(s);
      } else {
        for (const [s, pu] of this.svcEdges) if (s === sel.uid) rel.add(pu);
        for (const [su, nu] of this.svcNodeEdges) if (su === sel.uid) rel.add(nu);
      }
    }
    this.related = rel;
  }

  /** Zooms to show everything. */
  fit(userAction = true) {
    if (userAction) this.userMovedCamera = false;
    let minX = Infinity;
    let minY = Infinity;
    let maxX = -Infinity;
    let maxY = -Infinity;
    for (const s of this.sprites.values()) {
      if (s.removing) continue;
      const pad = s.kind === "node" ? (s.orbit ?? 40) : 30;
      minX = Math.min(minX, s.tx - pad);
      maxX = Math.max(maxX, s.tx + pad);
      minY = Math.min(minY, s.ty - pad);
      maxY = Math.max(maxY, s.ty + pad + 30);
    }
    if (!isFinite(minX) || !this.width) return;
    const k = Math.min(1.6, Math.max(0.15, Math.min(this.width / (maxX - minX + 60), this.height / (maxY - minY + 60))));
    this.cam = { k, x: this.width / 2 - ((minX + maxX) / 2) * k, y: this.height / 2 - ((minY + maxY) / 2) * k };
  }

  zoom(factor: number) {
    this.zoomAt(factor, this.width / 2, this.height / 2);
  }

  private zoomAt(factor: number, sx: number, sy: number) {
    const k = Math.min(4, Math.max(0.1, this.cam.k * factor));
    const wx = (sx - this.cam.x) / this.cam.k;
    const wy = (sy - this.cam.y) / this.cam.k;
    this.cam = { k, x: sx - wx * k, y: sy - wy * k };
    this.userMovedCamera = true;
  }

  private resize() {
    const host = this.canvas.parentElement ?? this.canvas;
    const r = host.getBoundingClientRect();
    this.dpr = window.devicePixelRatio || 1;
    this.width = r.width;
    this.height = r.height;
    this.canvas.width = Math.round(r.width * this.dpr);
    this.canvas.height = Math.round(r.height * this.dpr);
    this.canvas.style.width = `${r.width}px`;
    this.canvas.style.height = `${r.height}px`;
    if (!this.userMovedCamera) this.fit(false);
  }

  // --- input ---------------------------------------------------------------------

  private toWorld(e: { clientX: number; clientY: number }) {
    const r = this.canvas.getBoundingClientRect();
    const sx = e.clientX - r.left;
    const sy = e.clientY - r.top;
    return { sx, sy, x: (sx - this.cam.x) / this.cam.k, y: (sy - this.cam.y) / this.cam.k };
  }

  hitTest(x: number, y: number): Hit | null {
    let best: Hit | null = null;
    let bestD = Infinity;
    for (const s of this.sprites.values()) {
      if (s.removing || s.scale < 0.5) continue;
      const d = Math.hypot(s.x - x, s.y - y);
      const r = s.kind === "node" ? NODE_HALF + 4 : s.kind === "service" ? 16 : POD_R + 4;
      if (d <= r && d < bestD) {
        best = { kind: s.kind, uid: s.uid };
        bestD = d;
      }
    }
    return best;
  }

  private onDown = (e: PointerEvent) => {
    const w = this.toWorld(e);
    const hit = this.hitTest(w.x, w.y);
    if (hit?.kind === "node") {
      const s = this.sprites.get(hit.uid)!;
      this.drag = { mode: "node", uid: hit.uid, sx: w.sx, sy: w.sy, moved: false, ox: s.x, oy: s.y };
    } else {
      this.drag = { mode: "pan", sx: w.sx, sy: w.sy, moved: false, ox: this.cam.x, oy: this.cam.y };
    }
    this.canvas.setPointerCapture?.(e.pointerId);
  };

  private onMove = (e: PointerEvent) => {
    const w = this.toWorld(e);
    const d = this.drag;
    if (d) {
      const dx = w.sx - d.sx;
      const dy = w.sy - d.sy;
      if (Math.abs(dx) + Math.abs(dy) > 3) d.moved = true;
      if (!d.moved) return;
      if (d.mode === "pan") {
        this.cam.x = d.ox + dx;
        this.cam.y = d.oy + dy;
        this.userMovedCamera = true;
      } else if (d.uid) {
        const s = this.sprites.get(d.uid);
        if (s) {
          s.pinned = true;
          s.x = s.tx = d.ox + dx / this.cam.k;
          s.y = s.ty = d.oy + dy / this.cam.k;
          this.userMovedCamera = true;
          this.sync([]);
        }
      }
      return;
    }
    if (e.target !== this.canvas) return;
    const hit = this.hitTest(w.x, w.y);
    if (hit?.uid !== this.hover?.uid) {
      this.hover = hit;
      this.canvas.style.cursor = hit ? "pointer" : "grab";
    }
    this.onHover(hit, w.sx, w.sy);
  };

  private onUp = (e: PointerEvent) => {
    const d = this.drag;
    this.drag = null;
    if (!d || d.moved) return;
    const w = this.toWorld(e);
    const hit = this.hitTest(w.x, w.y);
    this.setSelected(hit);
    this.onSelect(hit);
  };

  private onLeave = () => {
    if (this.drag) return;
    this.hover = null;
    this.onHover(null, 0, 0);
  };

  private onWheel = (e: WheelEvent) => {
    e.preventDefault();
    const w = this.toWorld(e);
    this.zoomAt(Math.exp(-e.deltaY * 0.0015), w.sx, w.sy);
  };

  // --- drawing -------------------------------------------------------------------

  private podColor(p: K8sPod) {
    if (this.opts.colorBy === "namespace") return this.namespaceColor(p.namespace);
    switch (podHealth(p)) {
      case "ok":
        return this.pal.success;
      case "pending":
        return this.pal.warning;
      case "problem":
        return this.pal.destructive;
      default:
        return this.pal.muted;
    }
  }

  private frame = (t: number) => {
    this.raf = requestAnimationFrame(this.frame);
    const dt = Math.min(0.05, (t - this.last) / 1000);
    this.last = t;
    const ease = 1 - Math.exp(-dt * 5);
    for (const [uid, s] of this.sprites) {
      if (s.removing) {
        s.scale = Math.max(0, s.scale - dt * 2.2);
        if (t - s.removing > 900) this.sprites.delete(uid);
        continue;
      }
      if (!(this.drag?.mode === "node" && this.drag.uid === uid && this.drag.moved)) {
        s.x += (s.tx - s.x) * ease;
        s.y += (s.ty - s.y) * ease;
      }
      s.scale = Math.min(1, s.scale + dt * 2.5);
    }
    this.draw(t);
  };

  private draw(t: number) {
    const { ctx, pal, cam, dpr } = this;
    const m = this.model;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.fillStyle = pal.bg;
    ctx.fillRect(0, 0, this.width, this.height);

    // Dot grid that moves with the camera.
    const grid = 28 * cam.k;
    if (grid > 8) {
      ctx.fillStyle = pal.border;
      const ox = ((cam.x % grid) + grid) % grid;
      const oy = ((cam.y % grid) + grid) % grid;
      for (let x = ox; x < this.width; x += grid) for (let y = oy; y < this.height; y += grid) ctx.fillRect(x, y, 1.2, 1.2);
    }

    ctx.setTransform(dpr * cam.k, 0, 0, dpr * cam.k, dpr * cam.x, dpr * cam.y);
    const hasSel = this.related.size > 0;
    const dim = (uid: string) => (hasSel && !this.related.has(uid) ? 0.18 : 1);
    const q = this.opts.search.trim().toLowerCase();
    const matches = (name: string) => !q || name.toLowerCase().includes(q);
    const ease = (x: number) => 1 - Math.pow(1 - x, 3);

    // Node orbits and node → pod spokes.
    ctx.lineWidth = 1 / Math.max(cam.k, 0.5);
    for (const n of m.nodes.values()) {
      const s = this.sprites.get(n.uid);
      if (!s) continue;
      ctx.globalAlpha = 0.5 * dim(n.uid) * s.scale;
      ctx.strokeStyle = pal.border;
      ctx.setLineDash([3, 5]);
      ctx.beginPath();
      ctx.arc(s.x, s.y, (s.orbit ?? RING0) - 8, 0, Math.PI * 2);
      ctx.stroke();
      ctx.setLineDash([]);
    }
    ctx.strokeStyle = pal.muted;
    for (const p of this.visiblePods) {
      const ps = this.sprites.get(p.uid);
      const nUid = this.nodeUidByName.get(p.node);
      const ns = nUid && this.sprites.get(nUid);
      if (!ps || !ns || !nUid) continue;
      ctx.globalAlpha = 0.22 * ps.scale * (hasSel ? (this.related.has(p.uid) && this.related.has(nUid) ? 2.5 : 0.4) : 1);
      ctx.beginPath();
      ctx.moveTo(ns.x, ns.y);
      ctx.lineTo(ps.x, ps.y);
      ctx.stroke();
    }

    // Service → pod edges with traffic particles.
    if (this.svcEdges.length) {
      ctx.strokeStyle = pal.primary;
      ctx.setLineDash([4, 4]);
      for (const [su, pu] of this.svcEdges) {
        const a = this.sprites.get(su);
        const b = this.sprites.get(pu);
        if (!a || !b) continue;
        const on = !hasSel || (this.related.has(su) && this.related.has(pu));
        ctx.globalAlpha = (on ? 0.45 : 0.08) * Math.min(a.scale, b.scale);
        ctx.beginPath();
        ctx.moveTo(a.x, a.y);
        ctx.lineTo(b.x, b.y);
        ctx.stroke();
      }
      ctx.setLineDash([]);
      if (this.opts.traffic && this.svcEdges.length < 800) {
        ctx.fillStyle = pal.primary;
        for (const [su, pu] of this.svcEdges) {
          const a = this.sprites.get(su);
          const b = this.sprites.get(pu);
          const p = m.pods.get(pu);
          if (!a || !b || !p || podHealth(p) !== "ok") continue;
          const on = !hasSel || (this.related.has(su) && this.related.has(pu));
          if (!on) continue;
          const f = (t / 1600 + hash(su + pu)) % 1;
          ctx.globalAlpha = Math.sin(f * Math.PI) * 0.9 * Math.min(a.scale, b.scale);
          ctx.beginPath();
          ctx.arc(a.x + (b.x - a.x) * f, a.y + (b.y - a.y) * f, 2.2, 0, Math.PI * 2);
          ctx.fill();
        }
      }
    }

    // Service → node edges (selectorless services such as default/kubernetes):
    // longer dashes and a heavier stroke set them apart from service → pod.
    if (this.svcNodeEdges.length) {
      ctx.strokeStyle = pal.primary;
      ctx.lineWidth = 1.8;
      ctx.setLineDash([10, 5]);
      for (const [su, nu] of this.svcNodeEdges) {
        const a = this.sprites.get(su);
        const b = this.sprites.get(nu);
        if (!a || !b) continue;
        const on = !hasSel || (this.related.has(su) && this.related.has(nu));
        ctx.globalAlpha = (on ? 0.65 : 0.1) * Math.min(a.scale, b.scale);
        ctx.beginPath();
        ctx.moveTo(a.x, a.y);
        ctx.lineTo(b.x, b.y);
        ctx.stroke();
      }
      ctx.setLineDash([]);
      ctx.lineWidth = 1 / Math.max(cam.k, 0.5);
      if (this.opts.traffic) {
        ctx.fillStyle = pal.primary;
        for (const [su, nu] of this.svcNodeEdges) {
          const a = this.sprites.get(su);
          const b = this.sprites.get(nu);
          const n = m.nodes.get(nu);
          if (!a || !b || !n?.ready || (hasSel && !(this.related.has(su) && this.related.has(nu)))) continue;
          const f = (t / 1600 + hash(su + nu)) % 1;
          ctx.globalAlpha = Math.sin(f * Math.PI) * 0.9 * Math.min(a.scale, b.scale);
          ctx.beginPath();
          ctx.arc(a.x + (b.x - a.x) * f, a.y + (b.y - a.y) * f, 2.6, 0, Math.PI * 2);
          ctx.fill();
        }
      }
    }

    // Removed sprites of pods/services still fading: draw from their last known data if any.
    const drawPulse = (s: Sprite, radius: number) => {
      const age = (t - s.pulse) / 1200;
      if (!s.pulse || age < 0 || age > 1) return;
      ctx.globalAlpha = (1 - age) * 0.8;
      ctx.strokeStyle = s.pulseKind === "bad" ? pal.destructive : pal.primary;
      ctx.lineWidth = 2.5;
      ctx.beginPath();
      ctx.arc(s.x, s.y, radius + ease(age) * 26, 0, Math.PI * 2);
      ctx.stroke();
    };

    // Pods.
    const showPodLabels = cam.k > 1.5;
    for (const s of this.sprites.values()) {
      if (s.kind !== "pod") continue;
      const p = m.pods.get(s.uid);
      const sc = ease(s.scale);
      if (sc <= 0.01) continue;
      const r = POD_R * sc;
      const health = p ? podHealth(p) : "terminating";
      const color = p ? this.podColor(p) : pal.muted;
      let a = dim(s.uid) * (health === "terminating" ? 0.45 : 1) * (p && !matches(p.name) ? 0.25 : 1);
      if (s.removing) a *= s.scale;
      // Problem pods breathe.
      if (health === "problem" && !s.removing) {
        ctx.globalAlpha = a * (0.25 + 0.2 * Math.sin(t / 250));
        ctx.fillStyle = pal.destructive;
        ctx.beginPath();
        ctx.arc(s.x, s.y, r + 6, 0, Math.PI * 2);
        ctx.fill();
      }
      ctx.globalAlpha = a;
      ctx.fillStyle = pal.bg;
      ctx.beginPath();
      ctx.arc(s.x, s.y, r + 2, 0, Math.PI * 2);
      ctx.fill();
      ctx.fillStyle = color;
      ctx.beginPath();
      ctx.arc(s.x, s.y, r, 0, Math.PI * 2);
      ctx.fill();
      // Inner glyph: a small hexagon reads as "workload".
      ctx.fillStyle = pal.bg;
      ctx.globalAlpha = a * 0.85;
      ctx.beginPath();
      for (let i = 0; i < 6; i++) {
        const ang = Math.PI / 6 + (i * Math.PI) / 3;
        const px = s.x + Math.cos(ang) * r * 0.45;
        const py = s.y + Math.sin(ang) * r * 0.45;
        if (i === 0) ctx.moveTo(px, py);
        else ctx.lineTo(px, py);
      }
      ctx.closePath();
      ctx.fill();
      if (health === "pending" && !s.removing) {
        ctx.globalAlpha = a;
        ctx.strokeStyle = pal.warning;
        ctx.lineWidth = 1.6;
        ctx.setLineDash([3, 3]);
        ctx.lineDashOffset = -t / 60;
        ctx.beginPath();
        ctx.arc(s.x, s.y, r + 4.5, 0, Math.PI * 2);
        ctx.stroke();
        ctx.setLineDash([]);
        ctx.lineDashOffset = 0;
      }
      drawPulse(s, r);
      const isSel = this.selected?.uid === s.uid;
      const isHover = this.hover?.uid === s.uid;
      if (isSel || isHover || (q && p && matches(p.name))) {
        ctx.globalAlpha = 1;
        ctx.strokeStyle = pal.fg;
        ctx.lineWidth = isSel ? 2 : 1.2;
        ctx.beginPath();
        ctx.arc(s.x, s.y, r + 4, 0, Math.PI * 2);
        ctx.stroke();
      }
      if (p && (showPodLabels || isSel || isHover)) {
        ctx.globalAlpha = a;
        ctx.fillStyle = pal.muted;
        ctx.font = "500 7px ui-sans-serif, system-ui, sans-serif";
        ctx.textAlign = "center";
        ctx.fillText(p.name.length > 26 ? p.name.slice(0, 25) + "…" : p.name, s.x, s.y + r + 10);
      }
    }

    // Unscheduled queue label.
    if (this.hasUnscheduled) {
      ctx.globalAlpha = 0.9;
      ctx.fillStyle = pal.warning;
      ctx.font = "600 11px ui-sans-serif, system-ui, sans-serif";
      ctx.textAlign = "center";
      ctx.fillText("Aguardando o scheduler", this.unscheduledAnchor.x, this.unscheduledAnchor.y - 22);
    }

    // Services.
    for (const s of this.sprites.values()) {
      if (s.kind !== "service") continue;
      const svc = m.services.get(s.uid);
      const sc = ease(s.scale);
      if (sc <= 0.01) continue;
      const a = dim(s.uid) * (s.removing ? s.scale : 1) * (svc && !matches(svc.name) ? 0.3 : 1);
      const h = 13 * sc;
      ctx.globalAlpha = a;
      ctx.fillStyle = pal.bg;
      ctx.strokeStyle = pal.primary;
      ctx.lineWidth = 2;
      ctx.beginPath();
      ctx.moveTo(s.x, s.y - h);
      ctx.lineTo(s.x + h, s.y);
      ctx.lineTo(s.x, s.y + h);
      ctx.lineTo(s.x - h, s.y);
      ctx.closePath();
      ctx.fill();
      ctx.stroke();
      ctx.fillStyle = pal.primary;
      ctx.beginPath();
      ctx.arc(s.x, s.y, 3.2 * sc, 0, Math.PI * 2);
      ctx.fill();
      drawPulse(s, h);
      const isSel = this.selected?.uid === s.uid;
      if (isSel || this.hover?.uid === s.uid) {
        ctx.strokeStyle = pal.fg;
        ctx.lineWidth = isSel ? 2 : 1.2;
        ctx.strokeRect(s.x - h - 5, s.y - h - 5, (h + 5) * 2, (h + 5) * 2);
      }
      if (svc && cam.k > 0.55) {
        ctx.fillStyle = pal.fg;
        ctx.font = "500 9px ui-sans-serif, system-ui, sans-serif";
        ctx.textAlign = "center";
        ctx.fillText(svc.name, s.x, s.y + h + 12);
      }
    }

    // Nodes on top.
    for (const s of this.sprites.values()) {
      if (s.kind !== "node") continue;
      const n = m.nodes.get(s.uid);
      const sc = ease(s.scale);
      if (sc <= 0.01) continue;
      const a = dim(s.uid) * (s.removing ? s.scale : 1);
      const half = NODE_HALF * sc;
      const status = !n ? pal.muted : !n.ready ? pal.destructive : n.unschedulable ? pal.warning : pal.success;
      ctx.globalAlpha = a;
      ctx.fillStyle = pal.card;
      ctx.strokeStyle = status;
      ctx.lineWidth = 2.5;
      ctx.beginPath();
      ctx.roundRect(s.x - half, s.y - half, half * 2, half * 2, 9 * sc);
      ctx.fill();
      ctx.stroke();
      // Server glyph: three stacked bays with LEDs.
      ctx.fillStyle = pal.muted;
      for (let i = -1; i <= 1; i++) {
        const y = s.y + i * 9 * sc;
        ctx.globalAlpha = a * 0.9;
        ctx.beginPath();
        ctx.roundRect(s.x - 12 * sc, y - 3 * sc, 24 * sc, 6 * sc, 2 * sc);
        ctx.fill();
      }
      ctx.fillStyle = status;
      for (let i = -1; i <= 1; i++) {
        ctx.globalAlpha = a * (0.6 + 0.4 * Math.sin(t / 300 + i * 1.7 + hash(s.uid) * 6));
        ctx.beginPath();
        ctx.arc(s.x + 8 * sc, s.y + i * 9 * sc, 1.6 * sc, 0, Math.PI * 2);
        ctx.fill();
      }
      drawPulse(s, half);
      const isSel = this.selected?.uid === s.uid;
      if (isSel || this.hover?.uid === s.uid) {
        ctx.globalAlpha = 1;
        ctx.strokeStyle = pal.fg;
        ctx.lineWidth = isSel ? 2 : 1.2;
        ctx.beginPath();
        ctx.roundRect(s.x - half - 5, s.y - half - 5, (half + 5) * 2, (half + 5) * 2, 12);
        ctx.stroke();
      }
      if (n) {
        ctx.globalAlpha = a;
        ctx.textAlign = "center";
        const cp = n.roles?.includes("control-plane");
        const sub = `${n.internalIP}${cp ? " · control-plane" : ""}`;
        ctx.font = "600 12px ui-sans-serif, system-ui, sans-serif";
        const w1 = ctx.measureText(n.name).width;
        ctx.font = "500 9.5px ui-monospace, monospace";
        const w2 = ctx.measureText(sub).width;
        // A plate keeps the label legible over the pods orbiting below it.
        ctx.fillStyle = pal.card;
        ctx.globalAlpha = a * 0.92;
        ctx.beginPath();
        ctx.roundRect(s.x - Math.max(w1, w2) / 2 - 6, s.y + half + 4, Math.max(w1, w2) + 12, 30, 6);
        ctx.fill();
        ctx.globalAlpha = a;
        ctx.fillStyle = pal.fg;
        ctx.font = "600 12px ui-sans-serif, system-ui, sans-serif";
        ctx.fillText(n.name, s.x, s.y + half + 17);
        ctx.fillStyle = pal.muted;
        ctx.font = "500 9.5px ui-monospace, monospace";
        ctx.fillText(sub, s.x, s.y + half + 29);
      }
    }
    ctx.globalAlpha = 1;
  }
}
