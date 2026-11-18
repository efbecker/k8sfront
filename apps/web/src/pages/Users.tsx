import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useSearchParams } from "react-router-dom";
import {
  CheckCircle2,
  CircleAlert,
  Loader2,
  Network,
  Pencil,
  PlugZap,
  Plus,
  Save,
  ShieldCheck,
  Trash2,
  UserCheck,
  UserX,
  Users as UsersIcon,
  X,
} from "lucide-react";
import {
  api,
  roleLabel,
  type GroupMapping,
  type LdapConfig,
  type LdapInput,
  type LdapKind,
  type LdapReport,
  type LdapSecurity,
  type LdapView,
  type Role,
  type User,
  type UserInput,
} from "@/lib/api";
import { cn, formatDate } from "@/lib/utils";
import { PageHeader } from "@/components/layout/AppShell";
import { ConfirmButton, ErrorText, Field } from "@/components/deploy/shared";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input, NativeSelect, Textarea } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

const roles: Role[] = ["admin", "operator", "viewer"];

const roleHelp: Record<Role, string> = {
  admin: "Tudo, inclusive usuários e a configuração do LDAP/AD.",
  operator: "Cadastra servidores, provisiona e opera clusters, faz deploy, upgrades e baixa o kubeconfig.",
  viewer: "Só leitura: vê clusters, servidores, operações e logs. Não executa nada nem baixa o kubeconfig.",
};

const roleVariant: Record<Role, "default" | "info" | "secondary"> = { admin: "default", operator: "info", viewer: "secondary" };

export function RoleBadge({ role }: { role: Role }) {
  return <Badge variant={roleVariant[role]}>{roleLabel[role]}</Badge>;
}

function Check({ checked, onChange, children }: { checked: boolean; onChange: (v: boolean) => void; children: React.ReactNode }) {
  return (
    <label className="flex cursor-pointer items-start gap-2 text-sm">
      <input type="checkbox" className="accent-primary mt-0.5 size-4" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      <span>{children}</span>
    </label>
  );
}

// --- users ---------------------------------------------------------------------------

function UserDialog({ user, onClose }: { user?: User; onClose: () => void }) {
  const qc = useQueryClient();
  const ldapUser = user?.source === "ldap";
  const [username, setUsername] = useState(user?.username ?? "");
  const [displayName, setDisplayName] = useState(user?.displayName ?? "");
  const [email, setEmail] = useState(user?.email ?? "");
  const [role, setRole] = useState<Role>(user?.role ?? "viewer");
  const [password, setPassword] = useState("");
  const save = useMutation({
    mutationFn: () => {
      const input: UserInput = { displayName, email };
      if (!ldapUser) input.role = role;
      if (password) input.password = password;
      return user ? api.updateUser(user.id, input) : api.createUser({ ...input, username });
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["users"] });
      onClose();
    },
  });
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{user ? `Editar ${user.username}` : "Novo usuário local"}</DialogTitle>
          <DialogDescription>
            {ldapUser
              ? "Usuário do LDAP/AD: senha e papel vêm do diretório e do mapeamento de grupos, atualizados a cada login."
              : "A senha é guardada só como hash argon2id. Trocar a senha encerra as sessões abertas do usuário."}
          </DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            save.mutate();
          }}
        >
          <Field label="Usuário" hint={user ? undefined : "Letras minúsculas, números, ponto, hífen, _ ou @. Não muda depois."}>
            <Input value={username} disabled={!!user} autoComplete="off" onChange={(e) => setUsername(e.target.value.toLowerCase())} placeholder="maria.souza" required />
          </Field>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Nome">
              <Input value={displayName} onChange={(e) => setDisplayName(e.target.value)} placeholder="Maria Souza" />
            </Field>
            <Field label="E-mail">
              <Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="maria@empresa.com" />
            </Field>
          </div>
          <Field label="Papel" hint={ldapUser ? "Definido pelos grupos do diretório." : roleHelp[role]}>
            <NativeSelect value={role} disabled={ldapUser} onChange={(e) => setRole(e.target.value as Role)}>
              {roles.map((r) => (
                <option key={r} value={r}>
                  {roleLabel[r]}
                </option>
              ))}
            </NativeSelect>
          </Field>
          {!ldapUser && (
            <Field label={user ? "Nova senha" : "Senha inicial"} hint={user ? "Deixe em branco para manter a atual. Mínimo de 10 caracteres." : "Mínimo de 10 caracteres."}>
              <Input type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} required={!user} />
            </Field>
          )}
          <ErrorText error={save.error} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancelar
            </Button>
            <Button type="submit" disabled={save.isPending || (!user && (!username || !password))}>
              {save.isPending && <Loader2 className="animate-spin" />} Salvar
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function UserRow({ u, self, onEdit }: { u: User; self: boolean; onEdit: () => void }) {
  const qc = useQueryClient();
  const done = () => qc.invalidateQueries({ queryKey: ["users"] });
  const toggle = useMutation({ mutationFn: () => api.updateUser(u.id, { disabled: !u.disabled }), onSuccess: done });
  const del = useMutation({ mutationFn: () => api.deleteUser(u.id), onSuccess: done });
  return (
    <TableRow className={cn(u.disabled && "opacity-60")}>
      <TableCell>
        <div className="font-medium">
          {u.displayName || u.username}
          {self && <span className="text-muted-foreground ml-1 text-xs">(você)</span>}
        </div>
        <div className="text-muted-foreground font-mono text-xs">{u.username}</div>
        {u.email && <div className="text-muted-foreground text-xs">{u.email}</div>}
      </TableCell>
      <TableCell>
        {u.source === "ldap" ? (
          <div className="max-w-64">
            <Badge variant="outline">LDAP/AD</Badge>
            {u.groups.length > 0 && (
              <div className="text-muted-foreground mt-1 truncate text-xs" title={u.groups.join("\n")}>
                {u.groups.length} grupo(s)
              </div>
            )}
          </div>
        ) : (
          <Badge variant="outline">Local</Badge>
        )}
      </TableCell>
      <TableCell>
        <RoleBadge role={u.role} />
      </TableCell>
      <TableCell className="text-sm">{u.disabled ? <Badge variant="destructive">desativado</Badge> : <Badge variant="success">ativo</Badge>}</TableCell>
      <TableCell className="text-sm">{u.lastLoginAt ? formatDate(u.lastLoginAt) : <span className="text-muted-foreground">nunca</span>}</TableCell>
      <TableCell className="text-right">
        <div className="flex justify-end gap-1">
          <Button variant="ghost" size="icon" title="Editar" onClick={onEdit}>
            <Pencil />
          </Button>
          {!self && (
            <Button
              variant="ghost"
              size="icon"
              title={u.disabled ? "Reativar" : "Desativar (encerra as sessões)"}
              disabled={toggle.isPending}
              onClick={() => toggle.mutate()}
            >
              {u.disabled ? <UserCheck /> : <UserX />}
            </Button>
          )}
          {!self && (
            <ConfirmButton
              variant="ghost"
              size="icon"
              destructive
              requires="admin"
              title={`Excluir ${u.username}?`}
              description={
                u.source === "ldap"
                  ? "O registro sai do k8sfront, mas a conta continua no diretório: se ela ainda estiver num grupo mapeado, volta no próximo login. Para bloquear, desative."
                  : "A conta e a senha são apagadas. O histórico na Auditoria continua."
              }
              confirmLabel="Excluir"
              onConfirm={() => del.mutate()}
            >
              <Trash2 className="text-destructive" />
            </ConfirmButton>
          )}
        </div>
        <ErrorText error={toggle.error ?? del.error} />
      </TableCell>
    </TableRow>
  );
}

function UsersTab() {
  const users = useQuery({ queryKey: ["users"], queryFn: api.users });
  const me = useQuery({ queryKey: ["me"], queryFn: api.me });
  const [editing, setEditing] = useState<User | "new">();
  const list = users.data ?? [];
  const localAdmins = list.filter((u) => u.source === "local" && u.role === "admin" && !u.disabled).length;
  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="grid flex-1 gap-2 sm:grid-cols-3">
          {roles.map((r) => (
            <div key={r} className="rounded-md border p-3">
              <RoleBadge role={r} />
              <p className="text-muted-foreground mt-1.5 text-xs">{roleHelp[r]}</p>
            </div>
          ))}
        </div>
        <Button onClick={() => setEditing("new")}>
          <Plus /> Novo usuário
        </Button>
      </div>
      {users.isSuccess && localAdmins === 0 && (
        <div className="border-warning/40 bg-warning/10 flex items-start gap-2 rounded-md border px-3 py-2 text-sm">
          <ShieldCheck className="text-warning mt-0.5 size-4 shrink-0" />
          <span>
            Crie um <strong>administrador local</strong> antes de depender do LDAP/AD. Ele é a sua entrada se o diretório cair; o k8sfront não deixa
            remover, desativar ou rebaixar o último. O token de administrador continua valendo como acesso de emergência.
          </span>
        </div>
      )}
      <Card className="py-0">
        <CardContent className="px-0">
          {users.isLoading ? (
            <Loader2 className="text-muted-foreground m-6 animate-spin" />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Usuário</TableHead>
                  <TableHead>Origem</TableHead>
                  <TableHead>Papel</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Último login</TableHead>
                  <TableHead />
                </TableRow>
              </TableHeader>
              <TableBody>
                {list.map((u) => (
                  <UserRow key={u.id} u={u} self={me.data?.id === u.id} onEdit={() => setEditing(u)} />
                ))}
                {list.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={6} className="text-muted-foreground py-8 text-center text-sm">
                      Nenhum usuário ainda: hoje só o token de administrador entra. Usuários do LDAP/AD aparecem aqui depois do primeiro login.
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          )}
          <ErrorText error={users.error} />
        </CardContent>
      </Card>
      {editing && <UserDialog user={editing === "new" ? undefined : editing} onClose={() => setEditing(undefined)} />}
    </div>
  );
}

// --- LDAP / AD -------------------------------------------------------------------------

type LdapForm = LdapConfig & { bindPassword: string; clearBindPassword: boolean };

const securityLabel: Record<LdapSecurity, string> = {
  ldaps: "LDAPS (TLS direto, porta 636)",
  starttls: "StartTLS (porta 389)",
  none: "Sem TLS (porta 389, senhas em texto puro)",
};

function toLdapInput(f: LdapForm): LdapInput {
  const { bindPassword, clearBindPassword, ...cfg } = f;
  const input: LdapInput = { ...cfg, port: Number(cfg.port) || 0, timeoutSeconds: Number(cfg.timeoutSeconds) || 10 };
  if (bindPassword) input.bindPassword = bindPassword;
  if (clearBindPassword) input.clearBindPassword = true;
  return input;
}

function StepList({ r }: { r: LdapReport }) {
  return (
    <div className="space-y-2">
      <ul className="space-y-1">
        {r.steps.map((s, i) => {
          const Icon = s.ok ? CheckCircle2 : CircleAlert;
          return (
            <li key={i} className="flex items-start gap-2 text-sm">
              <Icon className={cn("mt-0.5 size-4 shrink-0", s.ok ? "text-success" : "text-destructive")} />
              <span>
                <span className="font-medium">{s.name}:</span> <span className="text-muted-foreground break-all">{s.detail}</span>
              </span>
            </li>
          );
        })}
      </ul>
      {r.identity && (
        <div className="rounded-md border p-3 text-sm">
          <div>
            <span className="font-medium">{r.identity.displayName || r.identity.username}</span>{" "}
            <span className="text-muted-foreground font-mono text-xs">{r.identity.username}</span>{" "}
            {r.identity.role ? <RoleBadge role={r.identity.role} /> : <Badge variant="destructive">sem acesso</Badge>}
          </div>
          <div className="text-muted-foreground mt-1 font-mono text-xs break-all">{r.identity.dn}</div>
          {r.identity.groups.length > 0 && (
            <ul className="text-muted-foreground mt-2 max-h-40 space-y-0.5 overflow-y-auto font-mono text-xs">
              {r.identity.groups.map((g) => (
                <li key={g} className="break-all">
                  {g}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}

function MappingEditor({ value, onChange }: { value: GroupMapping[]; onChange: (v: GroupMapping[]) => void }) {
  const set = (i: number, p: Partial<GroupMapping>) => onChange(value.map((m, j) => (j === i ? { ...m, ...p } : m)));
  return (
    <div className="space-y-2">
      {value.map((m, i) => (
        <div key={i} className="flex gap-2">
          <Input value={m.group} onChange={(e) => set(i, { group: e.target.value })} placeholder="k8s-admins ou CN=k8s-admins,OU=Grupos,DC=empresa,DC=local" />
          <NativeSelect className="w-44 shrink-0" value={m.role} onChange={(e) => set(i, { role: e.target.value as Role })}>
            {roles.map((r) => (
              <option key={r} value={r}>
                {roleLabel[r]}
              </option>
            ))}
          </NativeSelect>
          <Button type="button" variant="ghost" size="icon" title="Remover" onClick={() => onChange(value.filter((_, j) => j !== i))}>
            <X />
          </Button>
        </div>
      ))}
      <Button type="button" variant="outline" size="sm" onClick={() => onChange([...value, { group: "", role: "viewer" }])}>
        <Plus /> Adicionar grupo
      </Button>
    </div>
  );
}

function LdapEditor({ view }: { view: LdapView }) {
  const qc = useQueryClient();
  const [f, setF] = useState<LdapForm>(() => {
    const { hasBindPassword: _h, defaults: _d, ...cfg } = view;
    return { ...cfg, bindPassword: "", clearBindPassword: false };
  });
  const set = (p: Partial<LdapForm>) => setF((x) => ({ ...x, ...p }));
  const [testUser, setTestUser] = useState("");
  const [testPassword, setTestPassword] = useState("");
  const save = useMutation({
    mutationFn: () => api.saveLdap(toLdapInput(f)),
    onSuccess: (v) => {
      qc.setQueryData(["ldap"], v);
      qc.invalidateQueries({ queryKey: ["auth-providers"] });
      set({ bindPassword: "", clearBindPassword: false });
    },
  });
  const test = useMutation({ mutationFn: () => api.testLdap({ ...toLdapInput(f), username: testUser, password: testPassword }) });

  const switchKind = (kind: LdapKind) => {
    // Swap the filters/attributes only when they still hold the other kind's defaults.
    const from = view.defaults[f.kind];
    const to = view.defaults[kind];
    const keys = ["userFilter", "usernameAttr", "displayNameAttr", "emailAttr", "groupFilter"] as const;
    const patch: Partial<LdapForm> = { kind };
    for (const k of keys) if (!f[k] || f[k] === from[k]) patch[k] = to[k] ?? "";
    set(patch);
  };
  const switchSecurity = (security: LdapSecurity) => {
    const std = (s: LdapSecurity) => (s === "ldaps" ? 636 : 389);
    set({ security, port: !f.port || f.port === std(f.security) ? std(security) : f.port });
  };
  const hasPassword = view.hasBindPassword && !f.clearBindPassword;

  return (
    <form
      className="space-y-6"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Network className="size-5" /> Servidor
          </CardTitle>
          <CardDescription>
            O login tenta primeiro as contas locais e depois o diretório. Contas locais com o mesmo nome têm prioridade, e o token de administrador
            continua funcionando se o diretório ficar fora do ar.
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4">
          <Check checked={f.enabled} onChange={(v) => set({ enabled: v })}>
            <span className="font-medium">Permitir login pelo LDAP/AD</span>
          </Check>
          <div className="grid gap-3 sm:grid-cols-3">
            <Field label="Tipo de diretório">
              <NativeSelect value={f.kind} onChange={(e) => switchKind(e.target.value as LdapKind)}>
                <option value="ad">Microsoft Active Directory</option>
                <option value="ldap">OpenLDAP / FreeIPA / 389-ds</option>
              </NativeSelect>
            </Field>
            <Field label="Servidor" hint="Host ou IP, sem ldap://." className="sm:col-span-2">
              <Input value={f.host} onChange={(e) => set({ host: e.target.value.trim() })} placeholder="dc01.empresa.local" />
            </Field>
            <Field label="Segurança" className="sm:col-span-2">
              <NativeSelect value={f.security} onChange={(e) => switchSecurity(e.target.value as LdapSecurity)}>
                {(Object.keys(securityLabel) as LdapSecurity[]).map((s) => (
                  <option key={s} value={s}>
                    {securityLabel[s]}
                  </option>
                ))}
              </NativeSelect>
            </Field>
            <Field label="Porta">
              <Input type="number" value={f.port || ""} onChange={(e) => set({ port: Number(e.target.value) })} />
            </Field>
          </div>
          {f.security === "none" && (
            <p className="border-warning/40 bg-warning/10 rounded-md border px-3 py-2 text-sm">
              Sem TLS, a senha de quem faz login e a da conta de serviço passam em texto puro pela rede. Use LDAPS ou StartTLS fora do laboratório.
            </p>
          )}
          {f.security !== "none" && (
            <>
              <Field label="CA do diretório (PEM, opcional)" hint="Para certificados emitidos por uma CA interna (comum no AD CS). Somada às CAs do sistema.">
                <Textarea value={f.caCert} onChange={(e) => set({ caCert: e.target.value })} placeholder="-----BEGIN CERTIFICATE-----" className="min-h-16" />
              </Field>
              <Check checked={f.skipVerify} onChange={(v) => set({ skipVerify: v })}>
                Ignorar verificação do certificado. Use só em laboratório: qualquer um no caminho pode capturar as senhas.
              </Check>
            </>
          )}
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Bind DN (conta de serviço)" hint="Conta só de leitura usada para buscar usuários e grupos.">
              <Input
                value={f.bindDn}
                autoComplete="off"
                onChange={(e) => set({ bindDn: e.target.value })}
                placeholder={f.kind === "ad" ? "CN=svc-k8sfront,OU=Servicos,DC=empresa,DC=local" : "uid=svc-k8sfront,ou=services,dc=empresa,dc=com"}
              />
            </Field>
            <Field label="Senha da conta de serviço" hint={hasPassword ? "Guardada no cofre. Deixe em branco para manter." : undefined}>
              <Input
                type="password"
                autoComplete="new-password"
                value={f.bindPassword}
                onChange={(e) => set({ bindPassword: e.target.value })}
                placeholder={hasPassword ? "•••••••• (guardada)" : ""}
              />
            </Field>
          </div>
          {view.hasBindPassword && (
            <Check checked={f.clearBindPassword} onChange={(v) => set({ clearBindPassword: v })}>
              Remover a senha guardada (bind anônimo)
            </Check>
          )}
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Base de busca" hint="Onde procurar os usuários.">
              <Input value={f.baseDn} onChange={(e) => set({ baseDn: e.target.value })} placeholder={f.kind === "ad" ? "DC=empresa,DC=local" : "dc=empresa,dc=com"} />
            </Field>
            <Field label="Base dos grupos (opcional)" hint="Vazio: a mesma base de busca.">
              <Input value={f.groupBaseDn} onChange={(e) => set({ groupBaseDn: e.target.value })} placeholder="OU=Grupos,DC=empresa,DC=local" />
            </Field>
          </div>
          <details className="rounded-md border p-3">
            <summary className="cursor-pointer text-sm font-medium">Filtros e atributos</summary>
            <div className="mt-3 grid gap-3">
              <Field label="Filtro de usuário" hint="{username} é trocado pelo que a pessoa digitou (com escape).">
                <Input className="font-mono text-xs" value={f.userFilter} onChange={(e) => set({ userFilter: e.target.value })} />
              </Field>
              <Field
                label="Filtro de grupos"
                hint={
                  f.kind === "ad"
                    ? "{dn} é o DN do usuário. A regra 1.2.840.113556.1.4.1941 inclui grupos aninhados. O memberOf do usuário também é lido."
                    : "{dn} é o DN do usuário e {username} o login. O memberOf do usuário também é lido, se o servidor tiver o overlay."
                }
              >
                <Input className="font-mono text-xs" value={f.groupFilter} onChange={(e) => set({ groupFilter: e.target.value })} />
              </Field>
              <div className="grid gap-3 sm:grid-cols-4">
                <Field label="Atributo de login">
                  <Input value={f.usernameAttr} onChange={(e) => set({ usernameAttr: e.target.value })} />
                </Field>
                <Field label="Atributo de nome">
                  <Input value={f.displayNameAttr} onChange={(e) => set({ displayNameAttr: e.target.value })} />
                </Field>
                <Field label="Atributo de e-mail">
                  <Input value={f.emailAttr} onChange={(e) => set({ emailAttr: e.target.value })} />
                </Field>
                <Field label="Timeout (s)">
                  <Input type="number" value={f.timeoutSeconds || ""} onChange={(e) => set({ timeoutSeconds: Number(e.target.value) })} />
                </Field>
              </div>
            </div>
          </details>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <UsersIcon className="size-5" /> Grupos e papéis
          </CardTitle>
          <CardDescription>
            O usuário recebe o maior papel entre os grupos dele. Vale o nome do grupo (CN) ou o DN completo. O papel é recalculado a cada login.
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4">
          <MappingEditor value={f.groupMappings} onChange={(groupMappings) => set({ groupMappings })} />
          <Field label="Quem não está em nenhum grupo mapeado" className="sm:max-w-md">
            <NativeSelect value={f.defaultRole} onChange={(e) => set({ defaultRole: e.target.value as "" | Role })}>
              <option value="">Não entra</option>
              <option value="viewer">Entra como Leitura</option>
              <option value="operator">Entra como Operador</option>
            </NativeSelect>
          </Field>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <PlugZap className="size-5" /> Testar conexão
          </CardTitle>
          <CardDescription>
            Usa o formulário como está, sem salvar. Sem usuário, testa conexão, TLS, bind e a base de busca. Com usuário e senha, faz o login completo e mostra os
            grupos e o papel que ele receberia.
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4">
          <div className="grid gap-3 sm:grid-cols-[1fr_1fr_auto] sm:items-end">
            <Field label="Usuário de teste (opcional)">
              <Input value={testUser} autoComplete="off" onChange={(e) => setTestUser(e.target.value)} placeholder="maria.souza" />
            </Field>
            <Field label="Senha">
              <Input type="password" autoComplete="new-password" value={testPassword} onChange={(e) => setTestPassword(e.target.value)} />
            </Field>
            <Button type="button" variant="outline" disabled={test.isPending || !f.host || !f.baseDn} onClick={() => test.mutate()}>
              {test.isPending ? <Loader2 className="animate-spin" /> : <PlugZap />} Testar
            </Button>
          </div>
          {test.data && <StepList r={test.data} />}
          <ErrorText error={test.error} />
        </CardContent>
      </Card>

      <div className="flex items-center justify-end gap-3">
        {save.isSuccess && !save.isPending && <span className="text-success text-sm">Configuração salva.</span>}
        <ErrorText error={save.error} />
        <Button type="submit" disabled={save.isPending}>
          {save.isPending ? <Loader2 className="animate-spin" /> : <Save />} Salvar configuração
        </Button>
      </div>
    </form>
  );
}

function LdapTab() {
  const ldap = useQuery({ queryKey: ["ldap"], queryFn: api.ldap, staleTime: Infinity });
  if (ldap.isLoading) return <Loader2 className="text-muted-foreground animate-spin" />;
  if (!ldap.data) return <ErrorText error={ldap.error} />;
  return <LdapEditor view={ldap.data} />;
}

export function UsersPage() {
  const [params, setParams] = useSearchParams();
  const tab = params.get("tab") === "ldap" ? "ldap" : "users";
  return (
    <>
      <PageHeader title="Usuários e acesso" description="Quem entra no k8sfront e o que cada um pode fazer. Toda mudança fica na Auditoria." />
      <div className="mb-6 flex gap-1 border-b">
        {(
          [
            ["users", "Usuários"],
            ["ldap", "LDAP / Active Directory"],
          ] as const
        ).map(([k, label]) => (
          <button
            key={k}
            type="button"
            className={cn(
              "-mb-px border-b-2 px-3 py-2 text-sm transition-colors",
              tab === k ? "border-primary text-foreground font-medium" : "text-muted-foreground hover:text-foreground border-transparent",
            )}
            onClick={() => setParams(k === "users" ? {} : { tab: k }, { replace: true })}
          >
            {label}
          </button>
        ))}
      </div>
      {tab === "users" ? <UsersTab /> : <LdapTab />}
    </>
  );
}
