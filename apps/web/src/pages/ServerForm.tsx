import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Loader2 } from "lucide-react";
import { api, type AuthMethod, type NodeRole, type Server, type ServerInput } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input, NativeSelect, Textarea } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

interface Props {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  server?: Server; // edit mode when set
  onSaved?: (s: Server) => void;
}

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1.5">
      <Label>{label}</Label>
      {children}
      {hint && <p className="text-muted-foreground text-xs">{hint}</p>}
    </div>
  );
}

/** Create/edit a Debian 13+ server. Secrets are write-only: they are sent once and never returned. */
export function ServerFormDialog({ open, onOpenChange, server, onSaved }: Props) {
  const editing = !!server;
  const qc = useQueryClient();
  const [name, setName] = useState(server?.name ?? "");
  const [host, setHost] = useState(server?.host ?? "");
  const [port, setPort] = useState(String(server?.port ?? 22));
  const [user, setUser] = useState(server?.sshUser ?? "root");
  const [auth, setAuth] = useState<AuthMethod>(server?.authMethod ?? "key");
  const [role, setRole] = useState<NodeRole>(server?.role ?? "worker");
  const [privateKey, setPrivateKey] = useState("");
  const [passphrase, setPassphrase] = useState("");
  const [password, setPassword] = useState("");
  const [sudoPassword, setSudoPassword] = useState("");
  const [clearSudo, setClearSudo] = useState(false);

  const save = useMutation({
    mutationFn: () => {
      const input: ServerInput = { name, host, port: Number(port), sshUser: user, authMethod: auth, role };
      if (auth === "key" && privateKey) {
        input.privateKey = privateKey;
        input.passphrase = passphrase;
      }
      if (auth === "password" && password) input.password = password;
      if (sudoPassword) input.sudoPassword = sudoPassword;
      if (clearSudo) input.clearSudoPassword = true;
      return editing ? api.updateServer(server!.id, input) : api.createServer(input);
    },
    onSuccess: (s) => {
      qc.invalidateQueries({ queryKey: ["servers"] });
      qc.setQueryData(["server", s.id], s);
      setPrivateKey("");
      setPassphrase("");
      setPassword("");
      setSudoPassword("");
      onOpenChange(false);
      onSaved?.(s);
    },
  });

  const keyPlaceholder = editing && server?.hasPrivateKey ? "Chave armazenada (cifrada). Cole uma nova para substituir." : "-----BEGIN OPENSSH PRIVATE KEY-----";
  const pwPlaceholder = editing && server?.hasPassword ? "•••••••• (armazenada; preencha para substituir)" : "";

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{editing ? `Editar ${server!.name}` : "Adicionar servidor Debian 13+"}</DialogTitle>
          <DialogDescription>
            Credenciais são cifradas com AES-256-GCM no backend e nunca retornam pela API.
          </DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            save.mutate();
          }}
        >
          <div className="grid gap-4 sm:grid-cols-[2fr_1fr]">
            <Field label="IP ou hostname">
              <Input value={host} onChange={(e) => setHost(e.target.value)} placeholder="10.0.0.11" required />
            </Field>
            <Field label="Porta SSH">
              <Input type="number" min={1} max={65535} value={port} onChange={(e) => setPort(e.target.value)} required />
            </Field>
          </div>
          <div className="grid gap-4 sm:grid-cols-3">
            <Field label="Nome">
              <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="cp-01" />
            </Field>
            <Field label="Usuário SSH">
              <Input value={user} onChange={(e) => setUser(e.target.value)} required />
            </Field>
            <Field label="Papel">
              <NativeSelect value={role} onChange={(e) => setRole(e.target.value as NodeRole)}>
                <option value="control-plane">Control Plane</option>
                <option value="worker">Worker</option>
                <option value="">Sem papel</option>
              </NativeSelect>
            </Field>
          </div>
          <Field label="Autenticação SSH">
            <div className="flex gap-2">
              {(["key", "password"] as const).map((m) => (
                <Button key={m} type="button" size="sm" variant={auth === m ? "default" : "outline"} onClick={() => setAuth(m)}>
                  {m === "key" ? "Chave privada (Ed25519 / RSA)" : "Senha"}
                </Button>
              ))}
            </div>
          </Field>
          {auth === "key" ? (
            <>
              <Field label="Chave privada">
                <Textarea value={privateKey} onChange={(e) => setPrivateKey(e.target.value)} placeholder={keyPlaceholder} rows={5} spellCheck={false} />
              </Field>
              <Field label="Passphrase da chave (opcional)">
                <Input type="password" autoComplete="off" value={passphrase} onChange={(e) => setPassphrase(e.target.value)} />
              </Field>
            </>
          ) : (
            <Field label="Senha SSH">
              <Input type="password" autoComplete="off" value={password} onChange={(e) => setPassword(e.target.value)} placeholder={pwPlaceholder} />
            </Field>
          )}
          <Field
            label="Senha do sudo (opcional)"
            hint="Deixe vazio para root ou NOPASSWD. Com autenticação por senha, a senha SSH é usada se esta ficar vazia. É injetada apenas via stdin (sudo -S), nunca em argumentos."
          >
            <Input
              type="password"
              autoComplete="off"
              value={sudoPassword}
              onChange={(e) => setSudoPassword(e.target.value)}
              placeholder={editing && server?.hasSudoPassword ? "•••••••• (armazenada)" : ""}
            />
            {editing && server?.hasSudoPassword && (
              <label className="text-muted-foreground flex items-center gap-2 text-xs">
                <input type="checkbox" checked={clearSudo} onChange={(e) => setClearSudo(e.target.checked)} /> Remover senha do sudo armazenada
              </label>
            )}
          </Field>
          {save.error && <p className="text-destructive text-sm">{save.error.message}</p>}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              Cancelar
            </Button>
            <Button disabled={save.isPending}>
              {save.isPending && <Loader2 className="animate-spin" />}
              {editing ? "Salvar" : "Adicionar"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
