import { lazy, Suspense, useEffect, useState, type ReactNode } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { CheckCircle2, CircleAlert, CircleDashed, Loader2, MinusCircle, PencilLine, Trash2 } from "lucide-react";
import type { ApplySummary, DeleteRequest, Job } from "@/lib/api";
import { api, jobKindLabel } from "@/lib/api";
import { JobStatusBadge } from "@/components/status";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

const LazyYamlEditor = lazy(() => import("@/components/deploy/YamlEditor"));

/** Monaco loaded on demand (it is the heaviest dependency of the SPA). */
export function YamlEditor(props: { value: string; onChange?: (v: string) => void; height?: number | string; readOnly?: boolean }) {
  return (
    <Suspense
      fallback={
        <div className="text-muted-foreground flex items-center gap-2 rounded-md border p-4 text-sm" style={{ height: props.height ?? 480 }}>
          <Loader2 className="size-4 animate-spin" /> Carregando editor…
        </div>
      }
    >
      <LazyYamlEditor {...props} />
    </Suspense>
  );
}

export function Field({ label, hint, children, className }: { label: string; hint?: string; children: ReactNode; className?: string }) {
  return (
    <div className={"space-y-1.5 " + (className ?? "")}>
      <Label>{label}</Label>
      {children}
      {hint && <p className="text-muted-foreground text-xs">{hint}</p>}
    </div>
  );
}

const actionInfo = {
  created: { label: "criado", dry: "será criado", variant: "success", icon: CheckCircle2 },
  configured: { label: "atualizado", dry: "será atualizado", variant: "info", icon: PencilLine },
  unchanged: { label: "sem mudança", dry: "sem mudança", variant: "secondary", icon: MinusCircle },
  deleted: { label: "excluído", dry: "será excluído", variant: "destructive", icon: Trash2 },
  absent: { label: "não existia", dry: "não existe", variant: "secondary", icon: CircleDashed },
  error: { label: "erro", dry: "erro", variant: "destructive", icon: CircleAlert },
} as const;

/** Per-object outcome of a dry-run, apply or delete. */
export function ApplyResults({ summary, mode = "apply" }: { summary: ApplySummary; mode?: "apply" | "delete" }) {
  const ok = summary.failed === 0;
  return (
    <div className="space-y-2">
      <div className={"rounded-md border px-3 py-2 text-sm " + (ok ? "border-success/40 bg-success/10" : "border-destructive/40 bg-destructive/10")}>
        {mode === "delete"
          ? summary.dryRun
            ? ok
              ? "Dry-run OK: a API aceitou a exclusão de todos os objetos. Nada foi excluído ainda."
              : `Dry-run encontrou ${summary.failed} erro(s). Os objetos com erro não serão excluídos.`
            : ok
              ? "Exclusão concluída. Pods e réplicas são removidos em segundo plano pelo Kubernetes."
              : `${summary.failed} objeto(s) não foram excluídos; os demais foram.`
          : summary.dryRun
          ? ok
            ? "Dry-run no servidor OK: a API validou todos os objetos (admission incluída) sem gravar nada."
            : `Dry-run encontrou ${summary.failed} erro(s). Nada foi aplicado.`
          : ok
            ? "Aplicado com server-side apply (field manager k8sfront)."
            : `${summary.failed} objeto(s) falharam; os demais foram aplicados.`}
      </div>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Objeto</TableHead>
            <TableHead>Namespace</TableHead>
            <TableHead>Resultado</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {summary.results.map((r, i) => {
            const a = actionInfo[r.action];
            return (
              <TableRow key={i}>
                <TableCell>
                  <span className="text-muted-foreground">{r.kind}/</span>
                  <span className="font-medium">{r.name}</span>
                </TableCell>
                <TableCell className="text-sm">{r.namespace || "—"}</TableCell>
                <TableCell>
                  <Badge variant={a.variant}>
                    <a.icon />
                    {summary.dryRun ? a.dry : a.label}
                  </Badge>
                  {r.error && <div className="text-destructive mt-1 max-w-xl text-xs break-words">{r.error}</div>}
                  {r.note && <div className="text-muted-foreground mt-1 max-w-xl text-xs break-words">{r.note}</div>}
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </div>
  );
}

/** Button that asks for confirmation before running a risky action. */
export function ConfirmButton({
  title,
  description,
  confirmLabel = "Confirmar",
  destructive,
  onConfirm,
  disabled,
  children,
  variant = "outline",
  size = "sm",
}: {
  title: string;
  description: ReactNode;
  confirmLabel?: string;
  destructive?: boolean;
  onConfirm: () => void;
  disabled?: boolean;
  children: ReactNode;
  variant?: "outline" | "default" | "destructive" | "ghost" | "secondary";
  size?: "sm" | "default" | "icon";
}) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button variant={variant} size={size} disabled={disabled} onClick={() => setOpen(true)}>
        {children}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{title}</DialogTitle>
            <DialogDescription asChild>
              <div className="space-y-2">{description}</div>
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setOpen(false)}>
              Voltar
            </Button>
            <Button
              variant={destructive ? "destructive" : "default"}
              onClick={() => {
                setOpen(false);
                onConfirm();
              }}
            >
              {confirmLabel}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

/**
 * Delete flow: opening runs a server dry-run that lists exactly what will be
 * removed; the operator then types the confirmation word to delete for real.
 */
export function DeleteDialog({
  clusterId,
  request,
  title,
  confirmWord,
  open,
  onOpenChange,
  onDeleted,
}: {
  clusterId: string;
  request: DeleteRequest;
  title: string;
  confirmWord: string;
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onDeleted?: () => void;
}) {
  const qc = useQueryClient();
  const [typed, setTyped] = useState("");
  const check = useMutation({ mutationFn: () => api.deleteObjects(clusterId, request, true) });
  const run = useMutation({
    mutationFn: () => api.deleteObjects(clusterId, request, false),
    onSuccess: () => {
      for (const k of ["workloads", "namespaces", "network", "overview", "helm"]) qc.invalidateQueries({ queryKey: [k, clusterId] });
      onDeleted?.();
    },
  });
  useEffect(() => {
    if (open) {
      setTyped("");
      run.reset();
      check.mutate();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);
  const toDelete = check.data?.results.filter((r) => r.action === "deleted").length ?? 0;
  const done = run.data;
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>
            {done ? "Resultado da exclusão." : "A lista abaixo vem de um dry-run no servidor: é exatamente o que será removido. Não dá para desfazer."}
          </DialogDescription>
        </DialogHeader>
        <div className="max-h-[50vh] space-y-3 overflow-y-auto">
          {check.isPending && (
            <div className="text-muted-foreground flex items-center gap-2 text-sm">
              <Loader2 className="size-4 animate-spin" /> Verificando o que será excluído…
            </div>
          )}
          <ErrorText error={check.error ?? run.error} />
          {(done ?? check.data) && <ApplyResults summary={(done ?? check.data)!} mode="delete" />}
          {!done && toDelete > 0 && (
            <Field label={`Digite ${confirmWord} para confirmar`}>
              <Input value={typed} onChange={(e) => setTyped(e.target.value)} placeholder={confirmWord} autoComplete="off" />
            </Field>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {done ? "Fechar" : "Voltar"}
          </Button>
          {!done && (
            <Button variant="destructive" disabled={toDelete === 0 || typed.trim() !== confirmWord || run.isPending} onClick={() => run.mutate()}>
              {run.isPending ? <Loader2 className="animate-spin" /> : <Trash2 />} Excluir {toDelete > 0 ? `${toDelete} objeto(s)` : ""}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** Banner linking to a submitted job. */
export function JobLink({ job, text }: { job: Job; text?: string }) {
  return (
    <div className="bg-muted/50 flex flex-wrap items-center gap-2 rounded-md border px-3 py-2 text-sm">
      <JobStatusBadge status={job.status} />
      <span>{text ?? jobKindLabel[job.kind] ?? job.kind}</span>
      <Link to={`/jobs/${job.id}`} className="text-primary ml-auto font-medium hover:underline">
        Acompanhar no console →
      </Link>
    </div>
  );
}

export function ErrorText({ error }: { error: unknown }) {
  if (!error) return null;
  return <p className="text-destructive text-sm">{error instanceof Error ? error.message : String(error)}</p>;
}
