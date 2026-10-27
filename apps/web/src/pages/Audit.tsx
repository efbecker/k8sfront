import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { formatDate } from "@/lib/utils";
import { PageHeader } from "@/components/layout/AppShell";
import { Card, CardContent } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

export function AuditPage() {
  const audit = useQuery({ queryKey: ["audit"], queryFn: api.audit });
  return (
    <>
      <PageHeader title="Auditoria" description="Histórico de ações administrativas. Segredos nunca são registrados." />
      <Card className="py-0">
        <CardContent className="px-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Quando</TableHead>
                <TableHead>Ação</TableHead>
                <TableHead>Alvo</TableHead>
                <TableHead>Origem</TableHead>
                <TableHead>Detalhes</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {(audit.data ?? []).map((e) => (
                <TableRow key={e.id}>
                  <TableCell className="text-sm">{formatDate(e.ts)}</TableCell>
                  <TableCell className="font-mono text-xs">{e.action}</TableCell>
                  <TableCell className="font-mono text-xs">{e.targetType ? `${e.targetType}/${e.targetId.slice(0, 8)}` : "—"}</TableCell>
                  <TableCell className="text-sm">{e.remoteAddr}</TableCell>
                  <TableCell className="text-muted-foreground max-w-md truncate font-mono text-xs">
                    {e.details ? JSON.stringify(e.details) : ""}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>
    </>
  );
}
