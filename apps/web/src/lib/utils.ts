import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

export function formatBytes(n?: number) {
  if (!n) return "—";
  const gib = n / 2 ** 30;
  return gib >= 1 ? `${gib.toFixed(1)} GiB` : `${Math.round(n / 2 ** 20)} MiB`;
}

export function formatDate(s?: string | null) {
  if (!s) return "—";
  return new Date(s).toLocaleString("pt-BR", { dateStyle: "short", timeStyle: "medium" });
}

export function duration(start?: string | null, end?: string | null) {
  if (!start) return "—";
  const ms = (end ? new Date(end) : new Date()).getTime() - new Date(start).getTime();
  const s = Math.max(0, Math.round(ms / 1000));
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`;
}
