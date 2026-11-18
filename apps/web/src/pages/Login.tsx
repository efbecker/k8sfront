import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRound, Loader2, LogIn } from "lucide-react";
import { api } from "@/lib/api";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ThemeToggle } from "@/components/theme";

type Mode = "user" | "token";

export function LoginPage() {
  const providers = useQuery({ queryKey: ["auth-providers"], queryFn: api.authProviders, retry: false });
  const [picked, setPicked] = useState<Mode>();
  // Before any user exists (and without LDAP/AD) only the admin token works.
  const mode: Mode = picked ?? (providers.data && !providers.data.users && !providers.data.ldap ? "token" : "user");
  const [token, setToken] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const navigate = useNavigate();
  const qc = useQueryClient();
  const login = useMutation({
    mutationFn: () => api.login(mode === "token" ? { token } : { username, password }),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["me"] });
      navigate("/", { replace: true });
    },
  });
  const dir = providers.data?.ldap ? (providers.data.ldapKind === "ad" ? "Active Directory" : "LDAP") : "";

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
          <div className="bg-muted mb-4 grid grid-cols-2 gap-1 rounded-md p-1 text-sm" role="tablist">
            {(["user", "token"] as const).map((m) => (
              <button
                key={m}
                type="button"
                role="tab"
                aria-selected={mode === m}
                className={cn("rounded px-2 py-1 transition-colors", mode === m ? "bg-background font-medium shadow-xs" : "text-muted-foreground hover:text-foreground")}
                onClick={() => {
                  setPicked(m);
                  login.reset();
                }}
              >
                {m === "user" ? "Usuário" : "Token de administrador"}
              </button>
            ))}
          </div>
          <form
            className="space-y-4"
            onSubmit={(e) => {
              e.preventDefault();
              login.mutate();
            }}
          >
            {mode === "user" ? (
              <>
                <div className="space-y-2">
                  <Label htmlFor="username">Usuário</Label>
                  <Input id="username" autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} autoFocus />
                  {dir && <p className="text-muted-foreground text-xs">Contas locais e do {dir} entram por aqui.</p>}
                </div>
                <div className="space-y-2">
                  <Label htmlFor="password">Senha</Label>
                  <Input id="password" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
                </div>
              </>
            ) : (
              <div className="space-y-2">
                <Label htmlFor="token">Token de administrador</Label>
                <Input id="token" type="password" autoComplete="current-password" value={token} onChange={(e) => setToken(e.target.value)} autoFocus />
                <p className="text-muted-foreground text-xs">
                  Acesso de emergência. Definido em <code>K8SFRONT_ADMIN_TOKEN</code> ou gerado em <code>data/admin.token</code> na primeira execução.
                </p>
              </div>
            )}
            {login.error && <p className="text-destructive text-sm">{login.error.message}</p>}
            <Button className="w-full" disabled={(mode === "token" ? !token : !username || !password) || login.isPending}>
              {login.isPending ? <Loader2 className="animate-spin" /> : mode === "token" ? <KeyRound /> : <LogIn />}
              Entrar
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
