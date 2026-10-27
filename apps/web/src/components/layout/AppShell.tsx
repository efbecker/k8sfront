import { NavLink, Outlet, useNavigate } from "react-router-dom";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  Boxes,
  ClipboardList,
  LayoutDashboard,
  LogOut,
  Network,
  Rocket,
  RefreshCcw,
  Server,
  ScrollText,
  type LucideIcon,
} from "lucide-react";
import { api } from "@/lib/api";
import { cn } from "@/lib/utils";
import { ThemeToggle } from "@/components/theme";
import { Button } from "@/components/ui/button";

interface NavItem {
  to: string;
  label: string;
  icon: LucideIcon;
  phase?: number; // future modules, shown disabled until delivered
}

const nav: { title: string; items: NavItem[] }[] = [
  {
    title: "Infraestrutura",
    items: [
      { to: "/servers", label: "Servidores", icon: Server },
      { to: "/jobs", label: "Operações", icon: ClipboardList },
      { to: "/audit", label: "Auditoria", icon: ScrollText },
    ],
  },
  {
    title: "Cluster",
    items: [
      { to: "/bootstrap", label: "Deploy do Cluster", icon: Rocket, phase: 2 },
      { to: "/overview", label: "Visão Geral", icon: LayoutDashboard, phase: 3 },
      { to: "/workloads", label: "Workloads", icon: Boxes, phase: 3 },
      { to: "/network", label: "Rede & Storage", icon: Network, phase: 3 },
      { to: "/apps", label: "Central de Deploy", icon: Rocket, phase: 4 },
      { to: "/updates", label: "Updates & Lifecycle", icon: RefreshCcw, phase: 5 },
    ],
  },
];

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
              <ul className="space-y-0.5">
                {group.items.map((item) =>
                  item.phase ? (
                    <li key={item.to}>
                      <span
                        className="text-muted-foreground/60 flex cursor-not-allowed items-center gap-2 rounded-md px-2 py-1.5 text-sm"
                        title={`Disponível na Fase ${item.phase}`}
                      >
                        <item.icon className="size-4" />
                        <span className="flex-1">{item.label}</span>
                        <span className="rounded border px-1 text-[10px]">F{item.phase}</span>
                      </span>
                    </li>
                  ) : (
                    <li key={item.to}>
                      <NavLink
                        to={item.to}
                        className={({ isActive }) =>
                          cn(
                            "flex items-center gap-2 rounded-md px-2 py-1.5 text-sm transition-colors",
                            isActive ? "bg-primary/10 text-primary font-medium" : "hover:bg-accent",
                          )
                        }
                      >
                        <item.icon className="size-4" />
                        {item.label}
                      </NavLink>
                    </li>
                  ),
                )}
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
