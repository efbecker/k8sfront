import { describe, expect, it } from "vitest";
import { podHealth, selects, TopologyModel, type TopoMessage } from "./topologyStream";
import type { K8sPod, K8sService } from "./api";

const pod = (uid: string, extra: Partial<K8sPod> = {}): K8sPod => ({
  uid,
  name: uid,
  namespace: "default",
  node: "n1",
  phase: "Running",
  ready: 1,
  containers: 1,
  restarts: 0,
  images: [],
  labels: { app: "web" },
  created: "",
  ...extra,
});
const at = "";

describe("TopologyModel", () => {
  it("applies snapshot and deltas and reports animated changes", () => {
    const m = new TopologyModel();
    m.apply({ type: "snapshot", nodes: [], pods: [pod("a")], services: [], at });
    expect(m.pods.size).toBe(1);
    let ch = m.apply({ type: "upsert", kind: "pod", pod: pod("b", { phase: "Pending", ready: 0 }), at } as TopoMessage);
    expect(ch.map((c) => c.action)).toEqual(["added"]);
    ch = m.apply({ type: "upsert", kind: "pod", pod: pod("b"), at } as TopoMessage);
    expect(ch[0]).toMatchObject({ action: "status", detail: "Running" });
    ch = m.apply({ type: "upsert", kind: "pod", pod: pod("b", { restarts: 1 }), at } as TopoMessage);
    expect(ch[0]?.action).toBe("restarted");
    ch = m.apply({ type: "delete", kind: "pod", uid: "a", at });
    expect(ch[0]?.action).toBe("removed");
    expect([...m.pods.keys()]).toEqual(["b"]);
  });

  it("diffs a resync snapshot against the previous state", () => {
    const m = new TopologyModel();
    m.apply({ type: "snapshot", pods: [pod("a")], at });
    const ch = m.apply({ type: "snapshot", pods: [pod("b")], at });
    expect(ch.map((c) => `${c.uid}:${c.action}`).sort()).toEqual(["a:removed", "b:added"]);
  });
});

describe("helpers", () => {
  it("classifies pod health", () => {
    expect(podHealth(pod("x"))).toBe("ok");
    expect(podHealth(pod("x", { reason: "CrashLoopBackOff", ready: 0 }))).toBe("problem");
    expect(podHealth(pod("x", { phase: "Pending", ready: 0 }))).toBe("pending");
    expect(podHealth(pod("x", { reason: "Terminating" }))).toBe("terminating");
  });

  it("matches service selectors within the namespace", () => {
    const svc: K8sService = { uid: "s", name: "web", namespace: "default", type: "ClusterIP", clusterIP: "", ports: [], selector: { app: "web" }, created: "" };
    expect(selects(svc, pod("x"))).toBe(true);
    expect(selects(svc, pod("x", { namespace: "other" }))).toBe(false);
    expect(selects({ ...svc, selector: {} }, pod("x"))).toBe(false);
  });
});
