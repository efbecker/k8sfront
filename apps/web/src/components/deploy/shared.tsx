import { lazy, Suspense, useEffect, useState, type ReactNode } from "react";
import { useMutation, useQueries, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { CheckCircle2, CircleAlert, CircleDashed, Loader2, MinusCircle, PencilLine, Trash2 } from "lucide-react";
import type { ApplySummary, DeleteRequest, Job, NSItem, ObjectRef } from "@/lib/api";
import { api, jobKindLabel } from "@/lib/api";
import { JobStatusBadge } from "@/components/status";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

const systemNS = new Set(["kube-system", "kube-public", "kube-node-lease"]);

/** Objects the backend refuses to delete (cluster plumbing). */
export function isProtected(r: ObjectRef) {
  return systemNS.has(r.namespace ?? "") || (r.kind === "Service" && r.namespace === "default" && r.name === "kubernetes");
}

/** Trailing table cell with a delete button (empty for protected objects). */
export function DeleteCell({ target, onDelete }: { target: ObjectRef; onDelete: (r: ObjectRef) => void }) {
  if (isProtected(target)) return <TableCell />;
  return (
    <TableCell className="text-right">
      <Button variant="ghost" size="icon" title={`Excluir ${target.kind} ${target.name}`} onClick={() => onDelete(target)}>
        <Trash2 className="text-destructive" />
      </Button>
    </TableCell>
  );
}

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
                <TableCell className="whitespace-normal break-all">
                  <span className="text-muted-foreground">{r.kind}/</span>
                  <span className="font-medium">{r.name}</span>
                </TableCell>
                <TableCell className="text-sm whitespace-normal break-all">{r.namespace || "—"}</TableCell>
                <TableCell className="whitespace-normal">
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
  extra,
}: {
  clusterId: string;
  request: DeleteRequest;
  title: string;
  confirmWord: string;
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onDeleted?: () => void;
  /** Options shown above the dry-run result (changing request re-runs it). */
  extra?: ReactNode;
}) {
  const qc = useQueryClient();
  const [typed, setTyped] = useState("");
  // Namespaces the operator chose to delete too because they end up empty.
  const [alsoNs, setAlsoNs] = useState<string[]>([]);
  const effective: DeleteRequest = alsoNs.length ? { ...request, alsoNamespaces: alsoNs } : request;
  const [checked, setChecked] = useState<ApplySummary>();
  const check = useMutation({ mutationFn: () => api.deleteObjects(clusterId, effective, true), onSuccess: setChecked });
  const run = useMutation({
    mutationFn: () => api.deleteObjects(clusterId, effective, false),
    onSuccess: () => {
      for (const k of ["workloads", "namespaces", "namespaces-usage", "network", "overview", "helm"]) qc.invalidateQueries({ queryKey: [k, clusterId] });
      onDeleted?.();
    },
  });
  const reqKey = JSON.stringify(effective);
  useEffect(() => {
    if (open) {
      run.reset();
      check.mutate();
    } else {
      setTyped("");
      setAlsoNs([]);
      setChecked(undefined);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, reqKey]);
  const toDelete = checked?.results.filter((r) => r.action === "deleted").length ?? 0;
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
        <div className="max-h-[55vh] space-y-3 overflow-y-auto">
          {!done && extra}
          {check.isPending && !checked && (
            <div className="text-muted-foreground flex items-center gap-2 text-sm">
              <Loader2 className="size-4 animate-spin" /> Verificando o que será excluído…
            </div>
          )}
          <ErrorText error={check.error ?? run.error} />
          {!done && checked && (
            <NamespaceImpact
              clusterId={clusterId}
              summary={checked}
              alsoNs={alsoNs}
              onToggle={(ns, on) => setAlsoNs((cur) => (on ? [...cur.filter((x) => x !== ns), ns] : cur.filter((x) => x !== ns)))}
            />
          )}
          {(done ?? checked) && <ApplyResults summary={(done ?? checked)!} mode="delete" />}
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
            <Button variant="destructive" disabled={toDelete === 0 || typed.trim() !== confirmWord || run.isPending || check.isPending} onClick={() => run.mutate()}>
              {run.isPending ? <Loader2 className="animate-spin" /> : <Trash2 />} Excluir {toDelete > 0 ? `${toDelete} objeto(s)` : ""}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

const kindLabel: Record<string, string> = { PersistentVolumeClaim: "PVC", HelmRelease: "release Helm" };
const itemText = (i: NSItem) => `${kindLabel[i.kind] ?? i.kind} ${i.name}`;

function listText(items: NSItem[], max = 8) {
  const shown = items.slice(0, max).map(itemText).join(", ");
  return items.length > max ? `${shown} e mais ${items.length - max}` : shown;
}

/**
 * What a deletion does to namespaces: a deleted namespace takes everything in
 * it (listed, PVCs in red); a namespace that ends up empty can be deleted too
 * (opt-in); one still in use says by what.
 */
function NamespaceImpact({
  clusterId,
  summary,
  alsoNs,
  onToggle,
}: {
  clusterId: string;
  summary: ApplySummary;
  alsoNs: string[];
  onToggle: (ns: string, on: boolean) => void;
}) {
  const deleted = summary.results.filter((r) => r.action === "deleted");
  const nsDeleted = deleted.filter((r) => r.kind === "Namespace").map((r) => r.name);
  const ownNs = nsDeleted.filter((n) => !alsoNs.includes(n));
  const touched = [...new Set(deleted.filter((r) => r.namespace).map((r) => r.namespace!))].filter((n) => !systemNS.has(n) && n !== "default" && !ownNs.includes(n));
  const names = [...new Set([...nsDeleted, ...touched])].sort();
  const usage = useQueries({
    queries: names.map((n) => ({ queryKey: ["namespaces-usage", clusterId, n], queryFn: () => api.namespaceUsage(clusterId, n).then((l) => l[0]), staleTime: 5_000 })),
  });
  const byName = new Map(names.map((n, i) => [n, usage[i]?.data]));
  const pvcs = deleted.filter((r) => r.kind === "PersistentVolumeClaim");
  const boxes: ReactNode[] = [];
  if (pvcs.length > 0) {
    boxes.push(
      <Alert key="pvc">
        Os volumes {pvcs.map((r) => r.name).join(", ")} serão excluídos. Os dados neles podem ser perdidos de vez, conforme a reclaim policy da StorageClass.
      </Alert>,
    );
  }
  for (const n of ownNs) {
    const u = byName.get(n);
    if (!u) continue;
    const items = u.items.filter((i) => !deleted.some((d) => d.namespace === n && d.kind === i.kind && d.name === i.name));
    const nsPvcs = items.filter((i) => i.kind === "PersistentVolumeClaim");
    boxes.push(
      <div key={"ns-" + n} className={"rounded-md border px-3 py-2 text-sm " + (nsPvcs.length ? "border-destructive/40 bg-destructive/10" : "border-warning/40 bg-warning/10")}>
        {items.length === 0 ? (
          <>O namespace {n} será excluído (não há mais nada nele).</>
        ) : (
          <>
            Excluir o namespace <b>{n}</b> apaga também tudo o que está dentro dele ({u.pods} pod(s)): {listText(items)}.
            {nsPvcs.length > 0 && <div className="text-destructive mt-1 font-medium">Inclui {nsPvcs.length} PVC(s): os dados desses volumes podem ser perdidos.</div>}
          </>
        )}
      </div>,
    );
  }
  for (const n of touched) {
    const u = byName.get(n);
    if (!u) continue;
    const remaining = u.items.filter((i) => !deleted.some((d) => d.namespace === n && d.kind === i.kind && d.name === i.name));
    if (remaining.length === 0) {
      boxes.push(
        <label key={"t-" + n} className="flex cursor-pointer items-start gap-2 rounded-md border px-3 py-2 text-sm">
          <input type="checkbox" className="accent-primary mt-0.5 size-4" checked={alsoNs.includes(n)} onChange={(e) => onToggle(n, e.target.checked)} />
          <span>
            Excluir também o namespace <b>{n}</b>: depois desta exclusão não sobra nada nele.
          </span>
        </label>,
      );
    } else {
      boxes.push(
        <div key={"t-" + n} className="text-muted-foreground rounded-md border px-3 py-2 text-xs">
          O namespace <b className="text-foreground">{n}</b> continua, porque ainda está em uso por: {listText(remaining)}.
        </div>,
      );
    }
  }
  return boxes.length ? <div className="space-y-2">{boxes}</div> : null;
}

function Alert({ children }: { children: ReactNode }) {
  return (
    <div className="border-destructive/40 bg-destructive/10 flex gap-2 rounded-md border px-3 py-2 text-sm">
      <CircleAlert className="text-destructive mt-0.5 size-4 shrink-0" />
      <span>{children}</span>
    </div>
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
