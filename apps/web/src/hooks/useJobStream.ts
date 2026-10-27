import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { JobStream, type StreamState } from "@/lib/jobStream";
import type { Job, LogLine } from "@/lib/api";

/**
 * Subscribes to a job's live stream. Lines are pushed to `onLines` in small
 * batches (one per animation frame) so thousands of apt lines stay smooth.
 * The job snapshot is mirrored into the TanStack Query cache.
 */
export function useJobStream(jobId: string | undefined, onLines: (lines: LogLine[]) => void) {
  const qc = useQueryClient();
  const [state, setState] = useState<StreamState>("connecting");
  const [job, setJob] = useState<Job | undefined>();
  const sink = useRef(onLines);
  sink.current = onLines;

  useEffect(() => {
    if (!jobId) return;
    let buf: LogLine[] = [];
    let raf = 0;
    const flush = () => {
      raf = 0;
      const out = buf;
      buf = [];
      sink.current(out);
    };
    const stream = new JobStream(jobId, {
      onLog: (l) => {
        buf.push(l);
        if (!raf) raf = requestAnimationFrame(flush);
      },
      onJob: (j) => {
        setJob(j);
        qc.setQueryData(["job", j.id], j);
        qc.invalidateQueries({ queryKey: ["jobs"] });
        if (j.targetType === "server") qc.invalidateQueries({ queryKey: ["server", j.targetId] });
        qc.invalidateQueries({ queryKey: ["servers"] });
      },
      onState: setState,
    });
    stream.start();
    return () => {
      stream.stop();
      if (raf) cancelAnimationFrame(raf);
    };
  }, [jobId, qc]);

  return { state, job };
}
