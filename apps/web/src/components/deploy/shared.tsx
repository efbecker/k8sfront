import { lazy, Suspense, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { CheckCircle2, CircleAlert, Loader2, MinusCircle, PencilLine } from "lucide-react";
import type { ApplySummary, Job } from "@/lib/api";
import { jobKindLabel } from "@/lib/api";
import { JobStatusBadge } from "@/components/status";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
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
  error: { label: "erro", dry: "erro", variant: "destructive", icon: CircleAlert },
} as const;

/** Per-object outcome of a dry-run or apply. */
export function ApplyResults({ summary }: { summary: ApplySummary }) {
  const ok = summary.failed === 0;
  return (
    <div className="space-y-2">
      <div className={"rounded-md border px-3 py-2 text-sm " + (ok ? "border-success/40 bg-success/10" : "border-destructive/40 bg-destructive/10")}>
        {summary.dryRun
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
