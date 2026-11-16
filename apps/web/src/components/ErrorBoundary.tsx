import { Component, type ErrorInfo, type ReactNode } from "react";
import { AlertTriangle, RotateCw } from "lucide-react";
import { Button } from "@/components/ui/button";

/**
 * Keeps a crash in one page from blanking the whole app: shows the error
 * (message + stack, to copy into a bug report) and lets the user retry.
 */
export class ErrorBoundary extends Component<{ children: ReactNode }, { error?: Error; info?: string }> {
  state: { error?: Error; info?: string } = {};

  static getDerivedStateFromError(error: Error) {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("k8sfront: erro na página", error, info.componentStack);
    this.setState({ info: info.componentStack ?? "" });
  }

  render() {
    const { error, info } = this.state;
    if (!error) return this.props.children;
    const details = `${error.name}: ${error.message}\n\n${(error.stack ?? "").split("\n").slice(0, 8).join("\n")}\n\nComponentes:${(info ?? "").split("\n").slice(0, 12).join("\n")}`;
    return (
      <div className="border-destructive/40 bg-destructive/5 space-y-3 rounded-lg border p-5">
        <div className="text-destructive flex items-center gap-2 font-medium">
          <AlertTriangle className="size-5" /> Esta página encontrou um erro
        </div>
        <p className="text-sm">O restante da plataforma continua funcionando. Copie os detalhes abaixo para reportar o problema.</p>
        <pre className="bg-muted max-h-72 overflow-auto rounded-md p-3 text-xs whitespace-pre-wrap">{details}</pre>
        <div className="flex gap-2">
          <Button variant="outline" size="sm" onClick={() => this.setState({ error: undefined, info: undefined })}>
            <RotateCw /> Tentar de novo
          </Button>
          <Button variant="ghost" size="sm" onClick={() => navigator.clipboard?.writeText(details)}>
            Copiar detalhes
          </Button>
        </div>
      </div>
    );
  }
}
