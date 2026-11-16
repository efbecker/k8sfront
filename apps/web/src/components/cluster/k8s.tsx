import type { K8sNode, K8sPod } from "@/lib/api";
import { nodeStatus, podHealth, podStatus } from "@/lib/topologyStream";
import { Badge } from "@/components/ui/badge";
import { TableCell, TableRow } from "@/components/ui/table";

/** Compact age like kubectl: 45s, 12m, 3h, 9d. */
export function age(iso?: string) {
  if (!iso) return "—";
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return `${Math.floor(s)}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86400) return `${Math.floor(s / 3600)}h`;
  return `${Math.floor(s / 86400)}d`;
}

export function PodBadge({ p }: { p: K8sPod }) {
  const h = podHealth(p);
  const variant = h === "ok" ? "success" : h === "pending" ? "warning" : h === "problem" ? "destructive" : "secondary";
  return <Badge variant={variant}>{podStatus(p)}</Badge>;
}

export function NodeBadge({ n }: { n: K8sNode }) {
  const st = nodeStatus(n);
  return <Badge variant={st === "Ready" ? "success" : st === "Cordoned" ? "warning" : "destructive"}>{st === "Ready" ? "Pronto" : st}</Badge>;
}


export function Empty({ cols }: { cols: number }) {
  return (
    <TableRow>
      <TableCell colSpan={cols} className="text-muted-foreground py-8 text-center text-sm">
        Nada por aqui.
      </TableCell>
    </TableRow>
  );
}
