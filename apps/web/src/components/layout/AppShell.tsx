import { Navigate, NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import { useMutation, useQueryClient } from "@tanstack/react-query";
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
  Loader2,
  type LucideIcon,
} from "lucide-react";
import { api } from "@/lib/api";
import { cn } from "@/lib/utils";
import { ThemeToggle } from "@/components/theme";
import { Button } from "@/components/ui/button";
import { NativeSelect } from "@/components/ui/input";
import { rememberCluster, useActiveCluster } from "@/hooks/useActiveCluster";

interface NavItem {
  to: string;
  label: string;
  icon: LucideIcon;
  phase?: number; // future modules, shown disabled until delivered
  clusterPage?: string; // route under /clusters/:id/ of the active cluster
}

const nav: { title: string; items: NavItem[] }[] = [
  {
    title: "Infraestrutura",
    items: [
      { to: "/clusters", label: "Clusters", icon: Boxes },
      { to: "/servers", label: "Servidores", icon: Server },
      { to: "/jobs", label: "Operações", icon: ClipboardList },
      { to: "/audit", label: "Auditoria", icon: ScrollText },
    ],
  },
  {
    title: "Cluster",
    items: [
      { to: "/clusters/new", label: "Wizard de Deploy", icon: Rocket },
      { to: "/overview", label: "Visão Geral", icon: LayoutDashboard, clusterPage: "overview" },
      { to: "/topology", label: "Topologia ao vivo", icon: Share2, clusterPage: "topology" },
      { to: "/workloads", label: "Workloads", icon: Container, clusterPage: "workloads" },
      { to: "/network", label: "Rede & Storage", icon: Network, clusterPage: "network" },
      { to: "/deploy", label: "Central de Deploy", icon: PackagePlus, clusterPage: "deploy" },
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

export function AppShell() {
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
            {nav[0]!.items.map((i) => (
              <NavLink key={i.to} to={i.to} className={({ isActive }) => cn("rounded px-2 py-1 text-sm", isActive && "bg-accent")}>
                {i.label}
              </NavLink>
            ))}
          </nav>
          <div className="ml-auto flex items-center gap-1">
            <ThemeToggle />
            <Button variant="ghost" size="icon" title="Sair" aria-label="Sair" onClick={() => logout.mutate()}>
              <LogOut />
            </Button>
          </div>
        </header>
        <main className="mx-auto w-full max-w-7xl flex-1 p-4 md:p-6">
          <Outlet />
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
