import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, Fingerprint, KeyRound, Loader2, Pencil, RotateCcw, Stethoscope, Trash2 } from "lucide-react";
import { api, ApiError, type PreflightReport } from "@/lib/api";
import { duration, formatBytes, formatDate } from "@/lib/utils";
import { PageHeader } from "@/components/layout/AppShell";
import { CheckBadge, CheckIcon, JobStatusBadge, roleLabel, sudoLabel } from "@/components/status";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { ServerFormDialog } from "./ServerForm";

function Row({ k, v }: { k: string; v: React.ReactNode }) {
  return (
    <div className="flex justify-between gap-4 py-1.5 text-sm">
      <span className="text-muted-foreground">{k}</span>
      <span className="text-right">{v}</span>
    </div>
  );
}

export function ReportView({ report }: { report: PreflightReport }) {
  const f = report.facts;
  return (
    <div className="grid gap-6 lg:grid-cols-[3fr_2fr]">
      <ul className="divide-y rounded-lg border">
        {report.checks.map((c) => (
          <li key={c.id} className="flex items-start gap-3 p-3">
            <CheckIcon status={c.status} />
            <div className="min-w-0">
              <div className="text-sm font-medium">{c.name}</div>
              <div className="text-muted-foreground text-xs break-words">{c.detail}</div>
            </div>
          </li>
        ))}
      </ul>
      {f && (
        <div className="divide-y rounded-lg border px-3">
          <Row k="Hostname" v={f.hostname} />
          <Row k="Kernel" v={<span className="font-mono text-xs">{f.kernel}</span>} />
          <Row k="Arquitetura" v={f.arch} />
          <Row k="vCPU" v={f.cpus} />
          <Row k="Memória" v={formatBytes(f.memTotalBytes)} />
          <Row k="Disco livre (/var/lib)" v={formatBytes(f.diskFreeBytes)} />
          <Row k="Swap" v={f.swapBytes ? formatBytes(f.swapBytes) : "desativado"} />
          <Row k="Desvio de relógio" v={`${f.clockSkewSeconds.toFixed(3)}s (NTP: ${f.ntpSynchronized})`} />
          <Row k="Portas TCP em escuta" v={<span className="font-mono text-xs">{(f.listeningPorts ?? []).join(", ") || "—"}</span>} />
        </div>
      )}
    </div>
  );
}

export function ServerDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [editing, setEditing] = useState(false);
  const server = useQuery({ queryKey: ["server", id], queryFn: () => api.server(id) });
  const hostKey = useQuery({
    queryKey: ["server", id, "host-key"],
    queryFn: () => api.hostKey(id).catch((e) => (e instanceof ApiError && e.status === 404 ? null : Promise.reject(e))),
  });
  const jobs = useQuery({ queryKey: ["jobs", { targetId: id }], queryFn: () => api.jobs({ targetId: id, limit: 10 }) });

  const test = useMutation({ mutationFn: () => api.preflight(id), onSuccess: (j) => navigate(`/jobs/${j.id}`) });
  const del = useMutation({
    mutationFn: () => api.deleteServer(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["servers"] });
      navigate("/servers");
    },
  });
  const resetKey = useMutation({
    mutationFn: () => api.resetHostKey(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["server", id, "host-key"] }),
  });

  if (server.isLoading) return <Loader2 className="text-muted-foreground animate-spin" />;
  if (server.error || !server.data) return <p className="text-destructive">{server.error?.message ?? "Servidor não encontrado"}</p>;
  const s = server.data;

  return (
    <>
      <Link to="/servers" className="text-muted-foreground mb-3 inline-flex items-center gap-1 text-sm hover:underline">
        <ArrowLeft className="size-4" /> Servidores
      </Link>
      <PageHeader
        title={s.name}
        description={`${s.sshUser}@${s.host}:${s.port} · ${roleLabel(s.role)}`}
        actions={
          <>
            <Button variant="outline" onClick={() => setEditing(true)}>
              <Pencil /> Editar
            </Button>
            <Button
              variant="outline"
              className="text-destructive"
              disabled={del.isPending}
              onClick={() => confirm(`Remover ${s.name} do inventário? As credenciais cifradas serão apagadas.`) && del.mutate()}
            >
              <Trash2 /> Remover
            </Button>
            <Button disabled={test.isPending} onClick={() => test.mutate()}>
              {test.isPending ? <Loader2 className="animate-spin" /> : <Stethoscope />}
              Testar Conexão & Pré-requisitos
            </Button>
          </>
        }
      />
      {(test.error || del.error) && <p className="text-destructive mb-4 text-sm">{(test.error ?? del.error)?.message}</p>}

      <div className="grid gap-6 lg:grid-cols-3">
        <Card>
          <CardHeader>
            <CardTitle>Sistema</CardTitle>
          </CardHeader>
          <CardContent className="divide-y">
            <Row k="Debian" v={s.osPrettyName || "—"} />
            <Row k="Kernel" v={<span className="font-mono text-xs">{s.kernel || "—"}</span>} />
            <Row k="Privilégio" v={sudoLabel[s.sudoMode]} />
            <Row k="Último teste" v={<CheckBadge status={s.lastCheckStatus} />} />
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <KeyRound className="size-4" /> Credenciais
            </CardTitle>
            <CardDescription>Cifradas com AES-256-GCM; somente escrita.</CardDescription>
          </CardHeader>
          <CardContent className="divide-y">
            <Row k="Método" v={s.authMethod === "key" ? "Chave privada" : "Senha"} />
            {s.authMethod === "key" && <Row k="Passphrase" v={s.hasPassphrase ? "armazenada" : "não"} />}
            <Row k="Senha do sudo" v={s.hasSudoPassword ? "armazenada" : s.authMethod === "password" ? "usa a senha SSH" : "não"} />
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <Fingerprint className="size-4" /> Host key SSH
            </CardTitle>
            <CardDescription>Registrada no primeiro contato e verificada em toda conexão.</CardDescription>
            {hostKey.data && (
              <CardAction>
                <Button
                  size="sm"
                  variant="ghost"
                  title="Esquecer a chave (após reinstalação do host)"
                  onClick={() => confirm("Esquecer a host key registrada? A próxima conexão registrará a nova chave.") && resetKey.mutate()}
                >
                  <RotateCcw />
                </Button>
              </CardAction>
            )}
          </CardHeader>
          <CardContent>
            {hostKey.data ? (
              <div className="space-y-1 text-sm">
                <Badge variant="outline">{hostKey.data.keyType}</Badge>
                <div className="font-mono text-xs break-all">{hostKey.data.fingerprint}</div>
                <div className="text-muted-foreground text-xs">Primeiro contato: {formatDate(hostKey.data.firstSeen)}</div>
              </div>
            ) : (
              <p className="text-muted-foreground text-sm">Ainda não conectado.</p>
            )}
          </CardContent>
        </Card>
      </div>

      {s.lastCheckReport && (
        <Card className="mt-6">
          <CardHeader>
            <CardTitle>Último relatório de pré-requisitos</CardTitle>
            <CardDescription>{formatDate(s.lastCheckReport.checkedAt)}</CardDescription>
          </CardHeader>
          <CardContent>
            <ReportView report={s.lastCheckReport} />
          </CardContent>
        </Card>
      )}

      <Card className="mt-6">
        <CardHeader>
          <CardTitle>Operações recentes</CardTitle>
        </CardHeader>
        <CardContent>
          {(jobs.data ?? []).length === 0 ? (
            <p className="text-muted-foreground text-sm">Nenhuma operação ainda.</p>
          ) : (
            <ul className="divide-y">
              {jobs.data!.map((j) => (
                <li key={j.id} className="flex items-center justify-between py-2 text-sm">
                  <Link to={`/jobs/${j.id}`} className="hover:underline">
                    {j.kind} <span className="text-muted-foreground">· {formatDate(j.createdAt)}</span>
                  </Link>
                  <span className="flex items-center gap-3">
                    <span className="text-muted-foreground text-xs">{duration(j.startedAt, j.finishedAt)}</span>
                    <JobStatusBadge status={j.status} />
                  </span>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
      {editing && <ServerFormDialog open={editing} onOpenChange={setEditing} server={s} />}
    </>
  );
}
