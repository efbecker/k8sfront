import { describe, expect, it } from "vitest";
import { JobStream } from "./jobStream";
import type { LogLine } from "./api";

const line = (seq: number): LogLine => ({ jobId: "j", seq, ts: "", stream: "stdout", line: `l${seq}` });

describe("JobStream", () => {
  it("delivers each line once across replays and keeps the cursor", () => {
    const got: number[] = [];
    const s = new JobStream("j", { onLog: (l) => got.push(l.seq), onJob: () => {}, onState: () => {} }, 2);
    for (const seq of [1, 2, 3, 4, 3, 4, 5]) s.handle({ type: "log", log: line(seq) });
    expect(got).toEqual([3, 4, 5]);
    expect(s.cursor).toBe(5);
  });

  it("reports live state after backlog replay", () => {
    const states: string[] = [];
    const s = new JobStream("j", { onLog: () => {}, onJob: () => {}, onState: (st) => states.push(st) });
    s.handle({ type: "ready" });
    expect(states).toEqual(["live"]);
  });
});
