import { NavLink, Link } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { Activity, ArrowLeft, Container, LayoutDashboard, Network, PackagePlus, RefreshCcw, Share2 } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { cn } from "@/lib/utils";
import { ClusterStatusBadge } from "@/components/status";

const tabs = [
  { to: "overview", label: "Visão Geral", icon: LayoutDashboard },
  { to: "topology", label: "Topologia ao vivo", icon: Share2 },
  { to: "workloads", label: "Workloads", icon: Container },
  { to: "network", label: "Rede & Storage", icon: Network },
  { to: "deploy", label: "Central de Deploy", icon: PackagePlus },
  { to: "lifecycle", label: "Updates & Lifecycle", icon: RefreshCcw },
];

/** Title + tabs shared by the cluster management pages. */
export function ClusterHeader({ id, right }: { id: string; right?: React.ReactNode }) {
  const cluster = useQuery({ queryKey: ["cluster", id], queryFn: () => api.cluster(id), staleTime: 15_000 });
  const c = cluster.data;
  return (
    <div className="mb-5 space-y-4">
      <Link to={`/clusters/${id}`} className="text-muted-foreground inline-flex items-center gap-1 text-sm hover:underline">
        <ArrowLeft className="size-4" /> Detalhes do deploy
      </Link>
      <div className="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <h1 className="flex items-center gap-2 text-2xl font-semibold tracking-tight">
            <Activity className="text-primary size-5" />
            {c?.name ?? "Cluster"}
            {c && <ClusterStatusBadge status={c.status} />}
          </h1>
          {c && (
            <p className="text-muted-foreground mt-1 text-sm">
              Kubernetes {c.kubernetesVersion} · {c.cni === "cilium" ? "Cilium" : "Calico"} ·{" "}
              <span className="font-mono text-xs">{c.apiServerUrl || c.controlPlaneEndpoint}</span>
            </p>
          )}
        </div>
        {right && <div className="flex flex-wrap items-center gap-2">{right}</div>}
      </div>
      <nav className="flex gap-1 overflow-x-auto border-b">
        {tabs.map((t) => (
          <NavLink
            key={t.to}
            to={`/clusters/${id}/${t.to}`}
            className={({ isActive }) =>
              cn(
                "-mb-px flex shrink-0 items-center gap-1.5 border-b-2 px-3 py-2 text-sm transition-colors",
                isActive ? "border-primary text-foreground font-medium" : "text-muted-foreground hover:text-foreground border-transparent",
              )
            }
          >
            <t.icon className="size-4" />
            {t.label}
          </NavLink>
        ))}
      </nav>
    </div>
  );
}

/** Friendly error for the k8s endpoints. */
export function KubeError({ error }: { error: unknown }) {
  const msg =
    error instanceof ApiError && error.code === "not_ready"
      ? "Este cluster ainda não terminou o deploy. Acompanhe pela página de detalhes."
      : error instanceof Error
        ? error.message
        : "Erro desconhecido";
  return <div className="border-destructive/40 bg-destructive/10 text-destructive rounded-lg border p-4 text-sm">{msg}</div>;
}
