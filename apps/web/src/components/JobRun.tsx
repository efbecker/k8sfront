import { useCallback, useMemo, useRef, useState } from "react";
import { Wifi, WifiOff } from "lucide-react";
import type { JobStep, LogLine } from "@/lib/api";
import { cn } from "@/lib/utils";
import { useJobStream } from "@/hooks/useJobStream";
import { LogConsole, type LogConsoleHandle } from "@/components/LogConsole";
import { StepIcon } from "@/components/status";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";

const stateLabel = {
  connecting: "Conectando…",
  replaying: "Reproduzindo histórico…",
  live: "Ao vivo. A operação continua no backend se você fechar esta aba.",
  reconnecting: "Reconectando… (nenhuma linha será perdida)",
  closed: "Desconectado",
} as const;

/** Per-node stepper plus Xterm consoles (all nodes, or one node at a time) for a job. */
export function JobRun({ jobId, names }: { jobId: string; names: Map<string, string> }) {
  const consoles = useRef(new Map<string, LogConsoleHandle | null>());
  const [tab, setTab] = useState<string>("all");
  const nameOf = useCallback((id: string) => names.get(id), [names]);
  const onLines = useCallback((lines: LogLine[]) => {
    consoles.current.get("all")?.write(lines);
    const byNode = new Map<string, LogLine[]>();
    for (const l of lines) {
      if (!l.serverId) continue;
      byNode.set(l.serverId, [...(byNode.get(l.serverId) ?? []), l]);
    }
    for (const [id, ls] of byNode) consoles.current.get(id)?.write(ls);
  }, []);
  const { state, job } = useJobStream(jobId, onLines);

  const groups = useMemo(() => {
    const m = new Map<string, JobStep[]>();
    for (const st of job?.steps ?? []) {
      const k = st.serverId ?? "";
      m.set(k, [...(m.get(k) ?? []), st]);
    }
    return [...m.entries()];
  }, [job?.steps]);
  const nodeIds = [...names.keys()];
  const current = (steps: JobStep[]) => steps.filter((s) => s.status !== "skipped").at(-1);

  return (
    <div className="grid gap-6 xl:grid-cols-[340px_1fr]">
      <Card>
        <CardHeader>
          <CardTitle>Etapas por servidor</CardTitle>
        </CardHeader>
        <CardContent className="space-y-5">
          {groups.length === 0 && <p className="text-muted-foreground text-sm">Aguardando início…</p>}
          {groups.map(([sid, steps]) => {
            const cur = current(steps);
            return (
              <div key={sid || "cluster"}>
                <div className="mb-2 flex items-center justify-between text-xs font-medium tracking-wide uppercase">
                  <span>{sid ? (names.get(sid) ?? sid.slice(0, 8)) : "Cluster"}</span>
                  {cur && <StepIcon status={cur.status} />}
                </div>
                <ol className="space-y-1.5 border-l pl-3">
                  {steps.map((st) => (
                    <li key={st.id} className={cn("flex gap-2 text-sm", st.status === "skipped" && "opacity-60")}>
                      <StepIcon status={st.status} />
                      <div className="min-w-0">
                        <div className="leading-tight">{st.name.replace(/^.*?: /, "")}</div>
                        {st.status === "skipped" && <div className="text-muted-foreground text-xs">já concluída em tentativa anterior</div>}
                        {st.error && <div className="text-destructive text-xs break-words">{st.error}</div>}
                      </div>
                    </li>
                  ))}
                </ol>
              </div>
            );
          })}
        </CardContent>
      </Card>
      <Card className="min-w-0">
        <CardHeader>
          <CardTitle>Console</CardTitle>
          <CardDescription className="flex items-center gap-1.5">
            {state === "live" ? <Wifi className="text-success size-3.5" /> : <WifiOff className="size-3.5" />}
            {stateLabel[state]}
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          {nodeIds.length > 1 && (
            <div className="flex flex-wrap gap-1">
              {["all", ...nodeIds].map((id) => (
                <button
                  key={id}
                  onClick={() => setTab(id)}
                  className={cn("rounded-md border px-2.5 py-1 text-xs", tab === id ? "bg-primary text-primary-foreground border-primary" : "hover:bg-accent")}
                >
                  {id === "all" ? "Todos" : names.get(id)}
                </button>
              ))}
            </div>
          )}
          {["all", ...nodeIds].map((id) => (
            <div key={id} className={tab === id ? "" : "hidden"}>
              <LogConsole ref={(h) => void consoles.current.set(id, h)} serverName={id === "all" ? nameOf : undefined} />
            </div>
          ))}
        </CardContent>
      </Card>
    </div>
  );
}
