import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { NativeSelect } from "@/components/ui/input";

export function NamespacePicker({ clusterId, value, onChange }: { clusterId: string; value: string; onChange: (ns: string) => void }) {
  const ns = useQuery({ queryKey: ["namespaces", clusterId], queryFn: () => api.namespaces(clusterId), staleTime: 30_000 });
  return (
    <NativeSelect className="h-9 w-56" value={value} onChange={(e) => onChange(e.target.value)} aria-label="Namespace">
      <option value="">Todos os namespaces</option>
      {(ns.data ?? []).map((n) => (
        <option key={n} value={n}>
          {n}
        </option>
      ))}
    </NativeSelect>
  );
}

export function Tabs<T extends string>({ value, onChange, items }: { value: T; onChange: (v: T) => void; items: { id: T; label: string; count?: number }[] }) {
  return (
    <div className="bg-muted inline-flex rounded-lg p-1">
      {items.map((i) => (
        <button
          key={i.id}
          onClick={() => onChange(i.id)}
          className={
            "rounded-md px-3 py-1 text-sm transition-colors " +
            (value === i.id ? "bg-background text-foreground font-medium shadow-sm" : "text-muted-foreground hover:text-foreground")
          }
        >
          {i.label}
          {i.count != null && <span className="text-muted-foreground ml-1.5 text-xs tabular-nums">{i.count}</span>}
        </button>
      ))}
    </div>
  );
}
