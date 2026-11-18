import { Navigate, NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Boxes,
  Container,
  ClipboardList,
  LayoutDashboard,
  LogOut,
  Network,
  PackagePlus,
  Rocket,
  RefreshCcw,
  Server,
  ScrollText,
  Share2,
  Layers,
  Warehouse,
  Loader2,
  Users,
  Eye,
  KeyRound,
  type LucideIcon,
} from "lucide-react";
import { api, roleLabel, roleRank, type Me, type Role } from "@/lib/api";
import { RoleProvider, useCan } from "@/components/permissions";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { cn } from "@/lib/utils";
import { ThemeToggle } from "@/components/theme";
import { Button } from "@/components/ui/button";
import { NativeSelect } from "@/components/ui/input";
import { rememberCluster, useActiveCluster } from "@/hooks/useActiveCluster";
import { ErrorBoundary } from "@/components/ErrorBoundary";

interface NavItem {
  to: string;
  label: string;
  icon: LucideIcon;
  phase?: number; // future modules, shown disabled until delivered
  clusterPage?: string; // route under /clusters/:id/ of the active cluster
  minRole?: Role; // hidden below this role
}

const nav: { title: string; items: NavItem[] }[] = [
  {
    title: "Infraestrutura",
    items: [
      { to: "/clusters", label: "Clusters", icon: Boxes },
      { to: "/servers", label: "Servidores", icon: Server },
      { to: "/registries", label: "Registries", icon: Warehouse },
      { to: "/jobs", label: "Operações", icon: ClipboardList },
      { to: "/audit", label: "Auditoria", icon: ScrollText, minRole: "operator" },
      { to: "/users", label: "Usuários e acesso", icon: Users, minRole: "admin" },
    ],
  },
  {
    title: "Cluster",
    items: [
      { to: "/clusters/new", label: "Wizard de Deploy", icon: Rocket, minRole: "operator" },
      { to: "/overview", label: "Visão Geral", icon: LayoutDashboard, clusterPage: "overview" },
      { to: "/topology", label: "Topologia ao vivo", icon: Share2, clusterPage: "topology" },
      { to: "/workloads", label: "Workloads", icon: Container, clusterPage: "workloads" },
      { to: "/network", label: "Rede & Storage", icon: Network, clusterPage: "network" },
      { to: "/deploy", label: "Central de Deploy", icon: PackagePlus, clusterPage: "deploy" },
      { to: "/images", label: "Imagens", icon: Layers, clusterPage: "images" },
      { to: "/lifecycle", label: "Updates & Lifecycle", icon: RefreshCcw, clusterPage: "lifecycle" },
    ],
  },
];

const linkClass = ({ isActive }: { isActive: boolean }) =>
  cn(
    "flex items-center gap-2 rounded-md px-2 py-1.5 text-sm transition-colors",
    isActive ? "bg-primary/10 text-primary font-medium" : "hover:bg-accent",
  );

function Disabled({ item, title, tag }: { item: NavItem; title: string; tag?: string }) {
  return (
    <li>
      <span className="text-muted-foreground/60 flex cursor-not-allowed items-center gap-2 rounded-md px-2 py-1.5 text-sm" title={title}>
        <item.icon className="size-4" />
        <span className="flex-1">{item.label}</span>
        {tag && <span className="rounded border px-1 text-[10px]">{tag}</span>}
      </span>
    </li>
  );
}

function SidebarItem({ item }: { item: NavItem }) {
  const { active } = useActiveCluster();
  const { pathname } = useLocation();
  const allowed = useCan(item.minRole);
  if (!allowed) return null;
  if (item.phase) return <Disabled item={item} title={`Chega na Fase ${item.phase}`} tag={`F${item.phase}`} />;
  if (item.clusterPage) {
    if (!active) return <Disabled item={item} title="Provisione um cluster pelo Wizard de Deploy primeiro" />;
    return (
      <li>
        <NavLink to={`/clusters/${active.id}/${item.clusterPage}`} className={linkClass}>
          <item.icon className="size-4" />
          {item.label}
        </NavLink>
      </li>
    );
  }
  // "Clusters" stays highlighted on a cluster's deploy page, but not on the
  // wizard or on the per-cluster management pages, which have their own items.
  const clusterSub = /^\/clusters\/(new|[^/]+\/.+)/.test(pathname);
  return (
    <li>
      <NavLink to={item.to} className={({ isActive }) => linkClass({ isActive: isActive && !(item.to === "/clusters" && clusterSub) })}>
        <item.icon className="size-4" />
        {item.label}
      </NavLink>
    </li>
  );
}

/** Picks which cluster the "Cluster" menus manage (only shown with 2+ clusters). */
function ClusterSwitcher() {
  const { clusters, active } = useActiveCluster();
  const navigate = useNavigate();
  if (clusters.length < 2 || !active) return null;
  return (
    <NativeSelect
      className="mb-2 h-8 text-xs"
      value={active.id}
      aria-label="Cluster ativo"
      onChange={(e) => {
        rememberCluster(e.target.value);
        navigate(`/clusters/${e.target.value}/overview`);
      }}
    >
      {clusters.map((c) => (
        <option key={c.id} value={c.id}>
          {c.name}
        </option>
      ))}
    </NativeSelect>
  );
}

/** /overview, /topology... → the same page of the active cluster. */
export function ActiveClusterRedirect({ page }: { page: string }) {
  const { active, isLoading } = useActiveCluster();
  if (isLoading) return <Loader2 className="text-muted-foreground animate-spin" />;
  return <Navigate to={active ? `/clusters/${active.id}/${page}` : "/clusters"} replace />;
}

function Logo() {
  return (
    <div className="flex items-center gap-2 px-2">
      <img src="/favicon.svg" alt="" className="size-7" />
      <div className="leading-tight">
        <div className="text-sm font-semibold">k8sfront</div>
        <div className="text-muted-foreground text-[11px]">Kubernetes on Debian 13+</div>
      </div>
    </div>
  );
}

/** Local users change their own password here. */
function PasswordDialog({ onClose }: { onClose: () => void }) {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [again, setAgain] = useState("");
  const change = useMutation({ mutationFn: () => api.changePassword(current, next), onSuccess: onClose });
  const mismatch = again !== "" && again !== next;
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Trocar minha senha</DialogTitle>
          <DialogDescription>As outras sessões abertas com a sua conta são encerradas.</DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            change.mutate();
          }}
        >
          <div className="space-y-1.5">
            <Label htmlFor="pw-current">Senha atual</Label>
            <Input id="pw-current" type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="pw-new">Nova senha</Label>
            <Input id="pw-new" type="password" autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} />
            <p className="text-muted-foreground text-xs">Ao menos 10 caracteres.</p>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="pw-again">Repita a nova senha</Label>
            <Input id="pw-again" type="password" autoComplete="new-password" value={again} onChange={(e) => setAgain(e.target.value)} aria-invalid={mismatch} />
            {mismatch && <p className="text-destructive text-xs">As senhas não conferem.</p>}
          </div>
          {change.error && <p className="text-destructive text-sm">{change.error.message}</p>}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancelar
            </Button>
            <Button type="submit" disabled={!current || !next || next !== again || change.isPending}>
              {change.isPending && <Loader2 className="animate-spin" />} Salvar
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function UserBadge({ me }: { me: Me }) {
  const [changing, setChanging] = useState(false);
  const name = me.source === "token" ? "Token de administrador" : me.displayName || me.username;
  return (
    <div className="flex items-center gap-2">
      <div className="hidden text-right leading-tight sm:block">
        <div className="max-w-48 truncate text-sm font-medium" title={me.source === "token" ? undefined : me.username}>
          {name}
        </div>
        <div className="text-muted-foreground text-[11px]">
          {roleLabel[me.role]}
          {me.source === "ldap" ? " · LDAP/AD" : ""}
        </div>
      </div>
      {me.source === "local" && (
        <Button variant="ghost" size="icon" title="Trocar minha senha" aria-label="Trocar minha senha" onClick={() => setChanging(true)}>
          <KeyRound />
        </Button>
      )}
      {changing && <PasswordDialog onClose={() => setChanging(false)} />}
    </div>
  );
}

export function AppShell() {
  const { pathname } = useLocation();
  const me = useQuery({ queryKey: ["me"], queryFn: api.me, retry: false });
  const role: Role = me.data?.role ?? "viewer";
  return (
    <RoleProvider role={role}>
      <Shell me={me.data} pathname={pathname} />
    </RoleProvider>
  );
}

function Shell({ me, pathname }: { me?: Me; pathname: string }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const logout = useMutation({
    mutationFn: api.logout,
    onSuccess: () => {
      qc.clear();
      navigate("/login");
    },
  });

  return (
    <div className="flex min-h-screen">
      <aside className="bg-sidebar hidden w-60 shrink-0 flex-col border-r md:flex">
        <div className="flex h-14 items-center border-b px-3">
          <Logo />
        </div>
        <nav className="flex-1 space-y-6 overflow-y-auto p-3">
          {nav.map((group) => (
            <div key={group.title}>
              <div className="text-muted-foreground mb-1 px-2 text-[11px] font-medium tracking-wider uppercase">{group.title}</div>
              {group.title === "Cluster" && <ClusterSwitcher />}
              <ul className="space-y-0.5">
                {group.items.map((item) => (
                  <SidebarItem key={item.to} item={item} />
                ))}
              </ul>
            </div>
          ))}
        </nav>
      </aside>
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="bg-background/80 sticky top-0 z-10 flex h-14 items-center gap-2 border-b px-4 backdrop-blur">
          <div className="md:hidden">
            <Logo />
          </div>
          <nav className="flex gap-1 md:hidden">
            {nav[0]!.items.filter((i) => !i.minRole || (me && roleRank[me.role] >= roleRank[i.minRole])).map((i) => (
              <NavLink key={i.to} to={i.to} className={({ isActive }) => cn("rounded px-2 py-1 text-sm", isActive && "bg-accent")}>
                {i.label}
              </NavLink>
            ))}
          </nav>
          <div className="ml-auto flex items-center gap-1">
            {me && <UserBadge me={me} />}
            <ThemeToggle />
            <Button variant="ghost" size="icon" title="Sair" aria-label="Sair" onClick={() => logout.mutate()}>
              <LogOut />
            </Button>
          </div>
        </header>
        <main className="mx-auto w-full max-w-7xl flex-1 p-4 md:p-6">
          {me?.role === "viewer" && (
            <div className="bg-muted text-muted-foreground mb-4 flex items-center gap-2 rounded-md border px-3 py-2 text-sm">
              <Eye className="size-4 shrink-0" />
              <span>
                Seu papel é <strong className="text-foreground font-medium">Leitura</strong>. Você pode ver tudo, mas as ações ficam desativadas.
              </span>
            </div>
          )}
          <ErrorBoundary key={pathname}>
            <Outlet />
          </ErrorBoundary>
        </main>
      </div>
    </div>
  );
}

export function PageHeader({ title, description, actions }: { title: string; description?: string; actions?: React.ReactNode }) {
  return (
    <div className="mb-6 flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
        {description && <p className="text-muted-foreground mt-1 text-sm">{description}</p>}
      </div>
      {actions && <div className="flex flex-wrap gap-2">{actions}</div>}
    </div>
  );
}
