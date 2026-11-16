import { useEffect } from "react";
import { useMatch } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { api, type Cluster } from "@/lib/api";

const KEY = "k8sfront-active-cluster";

function stored() {
  try {
    return localStorage.getItem(KEY) ?? "";
  } catch {
    return "";
  }
}

/**
 * The cluster the "Cluster" menus operate on: the one in the URL, else the
 * last one visited, else the first ready cluster. Only clusters that already
 * have a kubeconfig can be managed.
 */
export function useActiveCluster(): { clusters: Cluster[]; active?: Cluster; isLoading: boolean } {
  const q = useQuery({ queryKey: ["clusters"], queryFn: api.clusters, staleTime: 15_000 });
  const routeId = useMatch("/clusters/:id/*")?.params.id;
  const usable = (q.data ?? []).filter((c) => c.hasKubeconfig);
  const saved = stored();
  const active =
    usable.find((c) => c.id === routeId) ??
    usable.find((c) => c.id === saved) ??
    usable.find((c) => c.status === "ready") ??
    usable[0];
  const activeId = active?.id;
  useEffect(() => {
    if (!activeId || activeId !== routeId) return;
    try {
      localStorage.setItem(KEY, activeId);
    } catch {
      /* private mode */
    }
  }, [activeId, routeId]);
  return { clusters: usable, active, isLoading: q.isLoading };
}

export function rememberCluster(id: string) {
  try {
    localStorage.setItem(KEY, id);
  } catch {
    /* private mode */
  }
}
