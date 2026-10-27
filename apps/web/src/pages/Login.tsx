import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { KeyRound, Loader2 } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ThemeToggle } from "@/components/theme";

export function LoginPage() {
  const [token, setToken] = useState("");
  const navigate = useNavigate();
  const qc = useQueryClient();
  const login = useMutation({
    mutationFn: () => api.login(token),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["me"] });
      navigate("/servers", { replace: true });
    },
  });

  return (
    <div className="relative flex min-h-screen items-center justify-center p-4">
      <div className="absolute top-4 right-4">
        <ThemeToggle />
      </div>
      <Card className="w-full max-w-sm">
        <CardHeader>
          <img src="/favicon.svg" alt="" className="mb-2 size-10" />
          <CardTitle className="text-xl">k8sfront</CardTitle>
          <CardDescription>Provisionamento e lifecycle de Kubernetes em Debian 13+.</CardDescription>
        </CardHeader>
        <CardContent>
          <form
            className="space-y-4"
            onSubmit={(e) => {
              e.preventDefault();
              login.mutate();
            }}
          >
            <div className="space-y-2">
              <Label htmlFor="token">Token de administrador</Label>
              <Input id="token" type="password" autoComplete="current-password" value={token} onChange={(e) => setToken(e.target.value)} autoFocus />
              <p className="text-muted-foreground text-xs">
                Definido em <code>K8SFRONT_ADMIN_TOKEN</code> ou gerado em <code>data/admin.token</code> na primeira execução.
              </p>
            </div>
            {login.error && <p className="text-destructive text-sm">{login.error.message}</p>}
            <Button className="w-full" disabled={!token || login.isPending}>
              {login.isPending ? <Loader2 className="animate-spin" /> : <KeyRound />}
              Entrar
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
