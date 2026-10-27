import { Link } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { Loader2 } from "lucide-react";
import { api } from "@/lib/api";
import { duration, formatDate } from "@/lib/utils";
import { PageHeader } from "@/components/layout/AppShell";
import { JobStatusBadge } from "@/components/status";
import { Card, CardContent } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

export function JobsPage() {
  const jobs = useQuery({ queryKey: ["jobs"], queryFn: () => api.jobs({ limit: 100 }), refetchInterval: 5000 });
  return (
    <>
      <PageHeader title="Operações" description="Jobs em background. Continuam rodando no backend mesmo com a aba fechada." />
      <Card className="py-0">
        <CardContent className="px-0">
          {jobs.isLoading ? (
            <Loader2 className="text-muted-foreground m-6 animate-spin" />
          ) : (jobs.data ?? []).length === 0 ? (
            <p className="text-muted-foreground p-6 text-sm">Nenhuma operação executada ainda.</p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Operação</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Criada</TableHead>
                  <TableHead>Duração</TableHead>
                  <TableHead>Tentativas</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {jobs.data!.map((j) => (
                  <TableRow key={j.id}>
                    <TableCell>
                      <Link to={`/jobs/${j.id}`} className="font-medium hover:underline">
                        {j.kind}
                      </Link>
                      <div className="text-muted-foreground font-mono text-xs">{j.id.slice(0, 8)}</div>
                    </TableCell>
                    <TableCell>
                      <JobStatusBadge status={j.status} />
                    </TableCell>
                    <TableCell className="text-sm">{formatDate(j.createdAt)}</TableCell>
                    <TableCell className="text-sm">{duration(j.startedAt, j.finishedAt)}</TableCell>
                    <TableCell className="text-sm">{j.attempts}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </>
  );
}
