import { AlertTriangle, CheckCircle2, CircleDashed, Loader2, PauseCircle, SkipForward, XCircle } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import type { CheckStatus, JobStatus, StepStatus, SudoMode } from "@/lib/api";

const jobMap: Record<JobStatus, { label: string; variant: "success" | "destructive" | "warning" | "info" | "secondary" }> = {
  pending: { label: "Na fila", variant: "secondary" },
  running: { label: "Executando", variant: "info" },
  succeeded: { label: "Concluído", variant: "success" },
  failed: { label: "Falhou", variant: "destructive" },
  paused: { label: "Pausado", variant: "warning" },
  cancelled: { label: "Abortado", variant: "secondary" },
};

export function JobStatusBadge({ status }: { status: JobStatus }) {
  const m = jobMap[status];
  return (
    <Badge variant={m.variant}>
      {status === "running" && <Loader2 className="animate-spin" />}
      {status === "paused" && <PauseCircle />}
      {m.label}
    </Badge>
  );
}

export function CheckBadge({ status }: { status: "" | CheckStatus }) {
  if (!status) return <Badge variant="outline">Não testado</Badge>;
  if (status === "pass")
    return (
      <Badge variant="success">
        <CheckCircle2 /> Aprovado
      </Badge>
    );
  if (status === "warn")
    return (
      <Badge variant="warning">
        <AlertTriangle /> Avisos
      </Badge>
    );
  return (
    <Badge variant="destructive">
      <XCircle /> Reprovado
    </Badge>
  );
}

export function CheckIcon({ status }: { status: CheckStatus }) {
  if (status === "pass") return <CheckCircle2 className="text-success size-4 shrink-0" />;
  if (status === "warn") return <AlertTriangle className="text-warning size-4 shrink-0" />;
  return <XCircle className="text-destructive size-4 shrink-0" />;
}

export function StepIcon({ status }: { status: StepStatus }) {
  switch (status) {
    case "running":
      return <Loader2 className="text-primary size-4 shrink-0 animate-spin" />;
    case "succeeded":
      return <CheckCircle2 className="text-success size-4 shrink-0" />;
    case "failed":
      return <XCircle className="text-destructive size-4 shrink-0" />;
    case "skipped":
      return <SkipForward className="text-muted-foreground size-4 shrink-0" />;
    default:
      return <CircleDashed className="text-muted-foreground size-4 shrink-0" />;
  }
}

export const sudoLabel: Record<SudoMode, string> = {
  unknown: "não detectado",
  root: "root direto",
  nopasswd: "sudo NOPASSWD",
  password: "sudo com senha",
};

export const roleLabel = (r: string) => (r === "control-plane" ? "Control Plane" : r === "worker" ? "Worker" : "Sem papel");

const clusterMap: Record<string, { label: string; variant: "success" | "destructive" | "info" | "secondary" }> = {
  draft: { label: "Rascunho", variant: "secondary" },
  provisioning: { label: "Provisionando", variant: "info" },
  ready: { label: "Pronto", variant: "success" },
  failed: { label: "Falhou", variant: "destructive" },
};

export function ClusterStatusBadge({ status }: { status: string }) {
  const m = clusterMap[status] ?? { label: status, variant: "secondary" as const };
  return (
    <Badge variant={m.variant}>
      {status === "provisioning" && <Loader2 className="animate-spin" />}
      {m.label}
    </Badge>
  );
}
