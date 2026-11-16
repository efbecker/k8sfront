import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CircleAlert, Loader2, Trash2 } from "lucide-react";
import { api, type RemovalPod, type Server } from "@/lib/api";
import { ErrorText, Field } from "@/components/deploy/shared";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";

const fateInfo: Record<RemovalPod["fate"], { label: string; variant: "info" | "secondary" | "destructive" | "outline" }> = {
  moves: { label: "vai para outro nó", variant: "info" },
  daemon: { label: "sai junto com o nó", variant: "secondary" },
  blocks: { label: "bloqueia o drain", variant: "destructive" },
  done: { label: "já terminou", variant: "outline" },
};

/**
 * "Remover servidor": a free server leaves the inventory only (the host is not
 * touched); a cluster worker is drained gracefully first (job), then deleted.
 */
export function RemoveServerDialog({ server, open, onOpenChange, onRemoved }: { server: Server; open: boolean; onOpenChange: (v: boolean) => void; onRemoved?: () => void }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [typed, setTyped] = useState("");
  useEffect(() => {
    if (!open) setTyped("");
  }, [open]);
  const plan = useQuery({ queryKey: ["server", server.id, "removal"], queryFn: () => api.serverRemoval(server.id), enabled: open, staleTime: 0 });
  const remove = useMutation({
    mutationFn: () => api.removeServer(server.id),
    onSuccess: (res) => {
      qc.invalidateQueries({ queryKey: ["servers"] });
      onOpenChange(false);
      if ("id" in res) navigate(`/jobs/${res.id}`);
      else onRemoved?.();
    },
  });
  const p = plan.data;
  const pods = p?.node?.pods ?? [];
  const count = (f: RemovalPod["fate"]) => pods.filter((x) => x.fate === f).length;
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Remover {server.name}?</DialogTitle>
          <DialogDescription>
            {p?.inCluster
              ? `${server.name} faz parte do cluster ${p.clusterName}. A remoção é gradual: nada é apagado antes de os pods saírem do nó.`
              : "O servidor sai do inventário e as credenciais cifradas são apagadas. Nada é executado na máquina."}
          </DialogDescription>
        </DialogHeader>
        <div className="max-h-[55vh] space-y-3 overflow-y-auto text-sm">
          {plan.isLoading && (
            <div className="text-muted-foreground flex items-center gap-2">
              <Loader2 className="size-4 animate-spin" /> Verificando o servidor no cluster…
            </div>
          )}
          <ErrorText error={plan.error ?? remove.error} />
          {p?.blocked && (
            <div className="border-destructive/40 bg-destructive/10 flex gap-2 rounded-md border px-3 py-2">
              <CircleAlert className="text-destructive mt-0.5 size-4 shrink-0" />
              <span>Não é possível remover: {p.blocked}</span>
            </div>
          )}
          {p?.inCluster && !p.blocked && (
            <ol className="list-decimal space-y-1 pl-5">
              <li>
                <b>cordon</b>: o nó{p.node ? ` ${p.node.nodeName}` : ""} para de receber pods novos.
              </li>
              <li>
                <b>drain</b> pela Eviction API, respeitando os PodDisruptionBudgets: os controladores recriam os pods em outros nós. Se algo travar, o job pausa sem excluir nada.
              </li>
              <li>
                <b>kubectl delete node</b> e <b>kubeadm reset</b> no servidor, para limpar kubelet, certificados e CNI.
              </li>
              <li>O servidor sai do inventário.</li>
            </ol>
          )}
          {p?.warnings.map((w) => (
            <div key={w} className="border-warning/40 bg-warning/10 rounded-md border px-3 py-2 text-xs">
              {w}
            </div>
          ))}
          {p?.node && !p.blocked && (
            <div className="space-y-1.5">
              <div className="text-muted-foreground text-xs">
                {pods.length} pod(s) no nó: {count("moves")} vão para outro nó, {count("daemon")} saem junto (DaemonSets/estáticos)
                {count("blocks") > 0 && `, ${count("blocks")} bloqueiam o drain`}. {p.node.otherSchedulable} outro(s) nó(s) disponível(is) para receber.
              </div>
              <div className="divide-y rounded-md border">
                {pods.map((x) => (
                  <div key={`${x.namespace}/${x.name}`} className="flex items-center gap-2 px-3 py-1.5">
                    <div className="min-w-0 flex-1">
                      <div className="truncate font-medium">{x.name}</div>
                      <div className="text-muted-foreground text-xs">
                        {x.namespace}
                        {x.ownerKind && ` · ${x.ownerKind}/${x.ownerName}`}
                        {x.emptyDir && " · emptyDir"}
                      </div>
                    </div>
                    <Badge variant={fateInfo[x.fate].variant}>{fateInfo[x.fate].label}</Badge>
                  </div>
                ))}
              </div>
            </div>
          )}
          {p && !p.blocked && (
            <Field label={`Digite ${server.name} para confirmar`}>
              <Input value={typed} onChange={(e) => setTyped(e.target.value)} placeholder={server.name} autoComplete="off" />
            </Field>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Voltar
          </Button>
          <Button variant="destructive" disabled={!p || !!p.blocked || typed.trim() !== server.name || remove.isPending} onClick={() => remove.mutate()}>
            {remove.isPending ? <Loader2 className="animate-spin" /> : <Trash2 />}
            {p?.inCluster ? "Drenar e remover" : "Remover do inventário"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
