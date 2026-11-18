import { Link } from "react-router-dom";
import { Lightbulb, Stethoscope } from "lucide-react";
import type { Check, CheckStatus, Server } from "@/lib/api";
import { formatDate } from "@/lib/utils";
import { CheckBadge, CheckIcon } from "@/components/status";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";

const order: Record<CheckStatus, number> = { fail: 0, warn: 1, pass: 2 };

/** What to do about a check that did not pass (Debian 13+ commands). */
export function checkHint(c: Check, inCluster: boolean): string | undefined {
  if (c.status === "pass") return undefined;
  switch (c.id) {
    case "connect":
      return "Confira IP, porta, usuário e senha/chave do SSH e se o firewall libera a porta. Se o servidor foi reinstalado, a host key mudou: redefina-a na página do servidor.";
    case "os":
      return "O k8sfront só gerencia Debian 13 (Trixie) ou superior. Atualize o sistema ou reinstale com Debian 13.";
    case "sudo":
      return "O usuário precisa de sudo. Adicione-o ao grupo sudo (usermod -aG sudo <usuário>) e cadastre a senha do sudo, se ela for diferente da senha do SSH.";
    case "facts":
      return "Não foi possível ler as informações do host. Teste de novo; se persistir, veja o log do teste em Operações.";
    case "arch":
      return "Use um servidor amd64 (x86_64) ou arm64 (aarch64).";
    case "cpu":
      return "Aumente o número de vCPUs da máquina (2 é o mínimo do kubeadm).";
    case "memory":
      return "Aumente a memória RAM da máquina até o mínimo indicado.";
    case "disk":
      return c.status === "warn"
        ? "Há espaço para instalar, mas abaixo do recomendado: imagens e atualizações ocupam /var/lib com o tempo. Aumente o disco ou libere espaço (apt-get clean, journalctl --vacuum-size=200M, crictl rmi --prune)."
        : "Libere espaço em /var/lib ou aumente o disco antes de continuar (apt-get clean, journalctl --vacuum-size=200M).";
    case "clock":
      return "Ative a sincronia de horário: apt-get install systemd-timesyncd && timedatectl set-ntp true. Relógio fora de sincronia quebra certificados e o etcd.";
    case "ports":
      return inCluster
        ? "Normal para um nó que já está no cluster: são as portas do próprio Kubernetes."
        : "Já existe algo usando as portas do Kubernetes (instalação anterior?). Rode kubeadm reset -f no servidor ou pare o serviço que usa as portas.";
    case "nodeports":
      return inCluster
        ? "Normal para um nó que já está no cluster: são Services do tipo NodePort em uso."
        : "Algum serviço escuta na faixa de NodePort do Kubernetes. Mude a porta dele para evitar conflito com Services NodePort.";
    case "swap":
      return "Nenhuma ação necessária: o bootstrap desativa o swap de forma persistente. Para resolver antes: swapoff -a e comente a linha de swap em /etc/fstab.";
    case "cgroup":
      return "O containerd precisa de cgroup v2 e systemd (padrão do Debian 13). Remova systemd.unified_cgroup_hierarchy=0 da linha do kernel no GRUB e reinicie.";
  }
  return undefined;
}

/** The checks of a server's last preflight, worst first, with what to do. */
export function CheckList({ checks, inCluster }: { checks: Check[]; inCluster: boolean }) {
  const sorted = [...checks].sort((a, b) => order[a.status] - order[b.status]);
  return (
    <ul className="divide-y rounded-lg border">
      {sorted.map((c) => {
        const hint = checkHint(c, inCluster);
        return (
          <li key={c.id} className="flex items-start gap-3 p-3">
            <CheckIcon status={c.status} />
            <div className="min-w-0 space-y-1">
              <div className="text-sm font-medium">{c.name}</div>
              {c.detail && <div className="text-muted-foreground text-xs break-words">{c.detail}</div>}
              {hint && (
                <div className="flex gap-1.5 text-xs">
                  <Lightbulb className="text-warning mt-px size-3.5 shrink-0" />
                  <span>{hint}</span>
                </div>
              )}
            </div>
          </li>
        );
      })}
    </ul>
  );
}

const summary: Record<CheckStatus, string> = {
  pass: "Todos os pré-requisitos foram atendidos.",
  warn: "O servidor pode ser usado. Os avisos abaixo não bloqueiam o bootstrap, mas vale resolver.",
  fail: "Há pré-requisitos com falha: resolva os itens em vermelho e teste de novo.",
};

export function PreflightDialog({
  server,
  open,
  onOpenChange,
  onTest,
}: {
  server: Server;
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onTest: () => void;
}) {
  const r = server.lastCheckReport;
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            Pré-requisitos de {server.name} <CheckBadge status={server.lastCheckStatus} />
          </DialogTitle>
          <DialogDescription>
            {r ? `${summary[r.status] ?? ""} Último teste em ${formatDate(r.checkedAt)}.` : "Este servidor ainda não foi testado."}
          </DialogDescription>
        </DialogHeader>
        {r && (
          <div className="max-h-[60vh] overflow-y-auto">
            <CheckList checks={r.checks} inCluster={!!server.clusterId} />
          </div>
        )}
        <DialogFooter>
          <Button variant="outline" asChild>
            <Link to={`/servers/${server.id}`}>Ver servidor</Link>
          </Button>
          <Button
            requires="operator"
            onClick={() => {
              onOpenChange(false);
              onTest();
            }}
          >
            <Stethoscope /> Testar de novo
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
