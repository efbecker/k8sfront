import { forwardRef, useEffect, useImperativeHandle, useRef } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import type { LogLine } from "@/lib/api";
import { useTheme } from "@/components/theme";

export interface LogConsoleHandle {
  write: (lines: LogLine[]) => void;
  clear: () => void;
}

const ESC = "\x1b[";
const color = {
  reset: `${ESC}0m`,
  dim: `${ESC}2m`,
  red: `${ESC}31m`,
  green: `${ESC}32m`,
  yellow: `${ESC}33m`,
  cyan: `${ESC}36m`,
  bold: `${ESC}1m`,
};

function render(l: LogLine, serverName?: (id: string) => string | undefined) {
  const t = new Date(l.ts).toLocaleTimeString("pt-BR", { hour12: false });
  const node = l.serverId ? serverName?.(l.serverId) : undefined;
  const prefix = `${color.dim}${t}${color.reset} ${node ? `${color.cyan}[${node}]${color.reset} ` : ""}`;
  let body = l.line;
  if (l.stream === "stderr") body = `${color.yellow}${body}${color.reset}`;
  else if (l.stream === "system") {
    if (body.startsWith("✖")) body = `${color.red}${color.bold}${body}${color.reset}`;
    else if (body.startsWith("✔")) body = `${color.green}${body}${color.reset}`;
    else if (body.startsWith("▶")) body = `${color.bold}${body}${color.reset}`;
    else if (body.startsWith("⏸") || body.includes("⚠")) body = `${color.yellow}${body}${color.reset}`;
  }
  return prefix + body;
}

/** Read-only Xterm.js console for job logs (ANSI colours, auto-fit, scrollback). */
export const LogConsole = forwardRef<LogConsoleHandle, { serverName?: (id: string) => string | undefined; className?: string }>(
  function LogConsole({ serverName, className }, ref) {
    const el = useRef<HTMLDivElement>(null);
    const term = useRef<Terminal | null>(null);
    const nameRef = useRef(serverName);
    nameRef.current = serverName;
    const { resolved } = useTheme();

    useEffect(() => {
      const t = new Terminal({
        convertEol: true,
        disableStdin: true,
        cursorBlink: false,
        cursorStyle: "bar",
        scrollback: 50000,
        fontSize: 12.5,
        fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
      });
      const fit = new FitAddon();
      t.loadAddon(fit);
      t.open(el.current!);
      fit.fit();
      term.current = t;
      const ro = new ResizeObserver(() => fit.fit());
      ro.observe(el.current!);
      return () => {
        ro.disconnect();
        t.dispose();
        term.current = null;
      };
    }, []);

    useEffect(() => {
      if (!term.current) return;
      term.current.options.theme =
        resolved === "dark"
          ? { background: "#0b0d12", foreground: "#e5e7eb", cursor: "#0b0d12" }
          : { background: "#fafafa", foreground: "#1f2937", cursor: "#fafafa", selectionBackground: "#bfdbfe" };
    }, [resolved]);

    useImperativeHandle(ref, () => ({
      write(lines) {
        if (!term.current || lines.length === 0) return;
        term.current.write(lines.map((l) => render(l, nameRef.current)).join("\n") + "\n");
      },
      clear() {
        term.current?.reset();
      },
    }));

    return <div ref={el} className={className ?? "h-[460px] w-full overflow-hidden rounded-lg border bg-[#fafafa] p-2 dark:bg-[#0b0d12]"} />;
  },
);
