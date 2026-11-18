import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useMutation, useQuery } from "@tanstack/react-query";
import { Loader2, Plus, ServerCog, Stethoscope, Trash2 } from "lucide-react";
import { api, type Server } from "@/lib/api";
import { RemoveServerDialog } from "@/components/servers/RemoveServerDialog";
import { PreflightDialog } from "@/components/servers/PreflightDialog";
import { formatDate } from "@/lib/utils";
import { PageHeader } from "@/components/layout/AppShell";
import { CheckBadge, roleLabel, sudoLabel } from "@/components/status";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { ServerFormDialog } from "./ServerForm";

export function ServersPage() {
  const navigate = useNavigate();
  const [adding, setAdding] = useState(false);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [removing, setRemoving] = useState<Server>();
  const [checking, setChecking] = useState<Server>();
  const servers = useQuery({ queryKey: ["servers"], queryFn: api.servers });
  const test = useMutation({
    mutationFn: (ids: string[]) => (ids.length === 1 ? api.preflight(ids[0]!) : api.preflightMany(ids)),
    onSuccess: (job) => navigate(`/jobs/${job.id}`),
  });

  const list = servers.data ?? [];
  const toggle = (id: string) =>
    setSelected((s) => {
      const n = new Set(s);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });

  return (
    <>
      <PageHeader
        title="Servidores"
        description="Inventário de nós Debian 13+ gerenciados via SSH."
        actions={
          <>
            <Button requires="operator" variant="outline" disabled={selected.size === 0 || test.isPending} onClick={() => test.mutate([...selected])}>
              {test.isPending ? <Loader2 className="animate-spin" /> : <Stethoscope />}
              Testar Conexão & Pré-requisitos{selected.size > 0 ? ` (${selected.size})` : ""}
            </Button>
            <Button requires="operator" onClick={() => setAdding(true)}>
              <Plus /> Adicionar servidor
            </Button>
          </>
        }
      />
      {test.error && <p className="text-destructive mb-4 text-sm">{test.error.message}</p>}
      <Card className="py-0">
        <CardContent className="px-0">
          {servers.isLoading ? (
            <div className="text-muted-foreground flex items-center gap-2 p-6 text-sm">
              <Loader2 className="size-4 animate-spin" /> Carregando…
            </div>
          ) : list.length === 0 ? (
            <div className="flex flex-col items-center gap-3 p-12 text-center">
              <ServerCog className="text-muted-foreground size-10" />
              <div className="font-medium">Nenhum servidor cadastrado</div>
              <p className="text-muted-foreground max-w-md text-sm">
                Cadastre os nós Debian 13 (Trixie) ou superior que formarão o cluster. A plataforma valida SSH, sudo, versão do Debian e
                pré-requisitos antes de qualquer operação.
              </p>
              <Button requires="operator" onClick={() => setAdding(true)}>
                <Plus /> Adicionar servidor
              </Button>
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-8">
                    <input
                      type="checkbox"
                      aria-label="Selecionar todos"
                      checked={selected.size === list.length}
                      onChange={(e) => setSelected(e.target.checked ? new Set(list.map((s) => s.id)) : new Set())}
                    />
                  </TableHead>
                  <TableHead>Servidor</TableHead>
                  <TableHead>Papel</TableHead>
                  <TableHead>Sistema</TableHead>
                  <TableHead>Acesso</TableHead>
                  <TableHead>Pré-requisitos</TableHead>
                  <TableHead className="text-right">Ações</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {list.map((s) => (
                  <TableRow key={s.id}>
                    <TableCell>
                      <input type="checkbox" aria-label={`Selecionar ${s.name}`} checked={selected.has(s.id)} onChange={() => toggle(s.id)} />
                    </TableCell>
                    <TableCell>
                      <Link to={`/servers/${s.id}`} className="font-medium hover:underline">
                        {s.name}
                      </Link>
                      <div className="text-muted-foreground font-mono text-xs">
                        {s.sshUser}@{s.host}:{s.port}
                      </div>
                    </TableCell>
                    <TableCell>
                      <Badge variant={s.role === "control-plane" ? "info" : "secondary"}>{roleLabel(s.role)}</Badge>
                    </TableCell>
                    <TableCell className="text-sm">
                      {s.osPrettyName || <span className="text-muted-foreground">—</span>}
                      {s.kernel && <div className="text-muted-foreground font-mono text-xs">{s.kernel}</div>}
                    </TableCell>
                    <TableCell className="text-sm">
                      <div>{s.authMethod === "key" ? "Chave SSH" : "Senha"}</div>
                      <div className="text-muted-foreground text-xs">{sudoLabel[s.sudoMode]}</div>
                    </TableCell>
                    <TableCell>
                      <button
                        type="button"
                        className="cursor-pointer rounded-md transition-opacity hover:opacity-80 focus-visible:ring-2 focus-visible:outline-none"
                        title="Ver os pré-requisitos e o que fazer"
                        onClick={() => setChecking(s)}
                      >
                        <CheckBadge status={s.lastCheckStatus} />
                      </button>
                      <div className="text-muted-foreground mt-0.5 text-xs">{formatDate(s.lastCheckAt)}</div>
                    </TableCell>
                    <TableCell className="text-right">
                      <Button requires="operator" size="sm" variant="outline" disabled={test.isPending} onClick={() => test.mutate([s.id])}>
                        <Stethoscope /> Testar
                      </Button>
                      <Button requires="operator" size="icon" variant="ghost" className="ml-1 size-8" title={`Remover ${s.name}`} onClick={() => setRemoving(s)}>
                        <Trash2 className="text-destructive" />
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
      {checking && (
        <PreflightDialog
          server={list.find((x) => x.id === checking.id) ?? checking}
          open
          onOpenChange={(v) => !v && setChecking(undefined)}
          onTest={() => test.mutate([checking.id])}
        />
      )}
      {removing && <RemoveServerDialog server={removing} open onOpenChange={(o) => !o && setRemoving(undefined)} />}
      {adding && <ServerFormDialog open={adding} onOpenChange={setAdding} onSaved={(s) => navigate(`/servers/${s.id}`)} />}
    </>
  );
}
