import { useCallback, useMemo, useRef } from "react";
import { Link, useParams } from "react-router-dom";
import { useMutation, useQuery } from "@tanstack/react-query";
import { ArrowLeft, Ban, Loader2, RotateCw, Wifi, WifiOff } from "lucide-react";
import { api, isFinal, type JobStep, type LogLine, type PreflightReport } from "@/lib/api";
import { duration, formatDate } from "@/lib/utils";
import { useJobStream } from "@/hooks/useJobStream";
import { PageHeader } from "@/components/layout/AppShell";
import { LogConsole, type LogConsoleHandle } from "@/components/LogConsole";
import { CheckBadge, JobStatusBadge, StepIcon } from "@/components/status";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { ReportView } from "./ServerDetail";

const kindLabel: Record<string, string> = { "servers.preflight": "Teste de conexão & pré-requisitos" };

export function JobDetailPage() {
  const { id = "" } = useParams();
  const consoleRef = useRef<LogConsoleHandle>(null);
  const servers = useQuery({ queryKey: ["servers"], queryFn: api.servers });
  const names = useMemo(() => new Map((servers.data ?? []).map((s) => [s.id, s.name])), [servers.data]);
  const serverName = useCallback((sid: string) => names.get(sid), [names]);
  const onLines = useCallback((lines: LogLine[]) => consoleRef.current?.write(lines), []);
  const initial = useQuery({ queryKey: ["job", id], queryFn: () => api.job(id) });
  const { state, job: live } = useJobStream(id, onLines);
  const job = live ?? initial.data;

  const cancel = useMutation({ mutationFn: () => api.cancelJob(id) });
  const retry = useMutation({ mutationFn: () => api.retryJob(id) });

  const groups = useMemo(() => {
    const m = new Map<string, JobStep[]>();
    for (const st of job?.steps ?? []) {
      const k = st.serverId ?? "";
      m.set(k, [...(m.get(k) ?? []), st]);
    }
    return [...m.entries()];
  }, [job?.steps]);

  if (initial.error) return <p className="text-destructive">{initial.error.message}</p>;
  if (!job) return <Loader2 className="text-muted-foreground animate-spin" />;

  const result = job.result as { reports?: Record<string, PreflightReport> } | undefined;
  const canRetry = job.status === "failed" || job.status === "paused" || job.status === "cancelled";
  const canCancel = !isFinal(job.status) || job.status === "paused";

  return (
    <>
      <Link to="/jobs" className="text-muted-foreground mb-3 inline-flex items-center gap-1 text-sm hover:underline">
        <ArrowLeft className="size-4" /> Operações
      </Link>
      <PageHeader
        title={kindLabel[job.kind] ?? job.kind}
        description={`Iniciado ${formatDate(job.startedAt ?? job.createdAt)} · duração ${duration(job.startedAt, job.finishedAt)} · tentativa ${job.attempts}`}
        actions={
          <>
            <JobStatusBadge status={job.status} />
            {canRetry && (
              <Button variant="outline" size="sm" disabled={retry.isPending} onClick={() => retry.mutate()}>
                <RotateCw /> Tentar novamente
              </Button>
            )}
            {canCancel && (
              <Button variant="outline" size="sm" className="text-destructive" disabled={cancel.isPending} onClick={() => cancel.mutate()}>
                <Ban /> {job.status === "paused" ? "Abortar fila" : "Cancelar"}
              </Button>
            )}
          </>
        }
      />
      {job.error && (
        <div className="border-destructive/40 bg-destructive/10 text-destructive mb-6 rounded-lg border p-3 text-sm">{job.error}</div>
      )}
      <div className="grid gap-6 lg:grid-cols-[300px_1fr]">
        <Card>
          <CardHeader>
            <CardTitle>Etapas</CardTitle>
          </CardHeader>
          <CardContent className="space-y-5">
            {groups.length === 0 && <p className="text-muted-foreground text-sm">Aguardando início…</p>}
            {groups.map(([sid, steps]) => (
              <div key={sid}>
                {sid && <div className="mb-2 text-xs font-medium tracking-wide uppercase">{names.get(sid) ?? sid.slice(0, 8)}</div>}
                <ol className="space-y-2">
                  {steps.map((st) => (
                    <li key={st.id} className="flex gap-2 text-sm">
                      <StepIcon status={st.status} />
                      <div className="min-w-0">
                        <div className="leading-tight">{st.name.replace(/^.*?: /, "")}</div>
                        {st.error && <div className="text-destructive text-xs break-words">{st.error}</div>}
                      </div>
                    </li>
                  ))}
                </ol>
              </div>
            ))}
          </CardContent>
        </Card>
        <Card className="min-w-0">
          <CardHeader>
            <CardTitle>Console</CardTitle>
            <CardDescription className="flex items-center gap-1.5">
              {state === "live" ? <Wifi className="text-success size-3.5" /> : <WifiOff className="size-3.5" />}
              {
                {
                  connecting: "Conectando…",
                  replaying: "Reproduzindo histórico…",
                  live: "Ao vivo. A operação continua no backend se você fechar esta aba.",
                  reconnecting: "Reconectando… (nenhuma linha será perdida)",
                  closed: "Desconectado",
                }[state]
              }
            </CardDescription>
          </CardHeader>
          <CardContent>
            <LogConsole ref={consoleRef} serverName={serverName} />
          </CardContent>
        </Card>
      </div>
      {result?.reports && (
        <div className="mt-6 space-y-6">
          {Object.entries(result.reports).map(([sid, rep]) => (
            <Card key={sid}>
              <CardHeader>
                <CardTitle className="flex items-center gap-2">
                  <Link to={`/servers/${sid}`} className="hover:underline">
                    {names.get(sid) ?? sid}
                  </Link>
                  <CheckBadge status={rep.status} />
                </CardTitle>
                {rep.os && <CardDescription>{rep.os.prettyName}</CardDescription>}
              </CardHeader>
              <CardContent>
                <ReportView report={rep} />
              </CardContent>
            </Card>
          ))}
        </div>
      )}
    </>
  );
}
