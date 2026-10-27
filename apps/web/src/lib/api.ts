// Typed client for the k8sfront control plane (apps/api).

export type AuthMethod = "key" | "password";
export type NodeRole = "" | "control-plane" | "worker";
export type SudoMode = "unknown" | "root" | "nopasswd" | "password";
export type JobStatus = "pending" | "running" | "succeeded" | "failed" | "paused" | "cancelled";
export type StepStatus = "pending" | "running" | "succeeded" | "failed" | "skipped";
export type CheckStatus = "pass" | "warn" | "fail";

export interface Server {
  id: string;
  clusterId: string | null;
  name: string;
  host: string;
  port: number;
  sshUser: string;
  authMethod: AuthMethod;
  role: NodeRole;
  sudoMode: SudoMode;
  osId: string;
  osVersionId: string;
  osPrettyName: string;
  kernel: string;
  lastCheckAt: string | null;
  lastCheckStatus: "" | CheckStatus;
  lastCheckReport?: PreflightReport;
  hasPrivateKey: boolean;
  hasPassphrase: boolean;
  hasPassword: boolean;
  hasSudoPassword: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface ServerInput {
  name?: string;
  host?: string;
  port?: number;
  sshUser?: string;
  authMethod?: AuthMethod;
  role?: NodeRole;
  privateKey?: string;
  passphrase?: string;
  password?: string;
  sudoPassword?: string;
  clearSudoPassword?: boolean;
}

export interface Check {
  id: string;
  name: string;
  status: CheckStatus;
  detail: string;
}

export interface PreflightReport {
  status: CheckStatus;
  role: NodeRole;
  os?: { major: number; versionId: string; codename: string; prettyName: string };
  sudo?: { mode: SudoMode; uid: string; message: string };
  facts?: {
    hostname: string;
    kernel: string;
    arch: string;
    cpus: number;
    memTotalBytes: number;
    diskFreeBytes: number;
    clockSkewSeconds: number;
    ntpSynchronized: string;
    swapBytes: number;
    listeningPorts: number[] | null;
  };
  checks: Check[];
  checkedAt: string;
}

export interface JobStep {
  id: number;
  jobId: string;
  seq: number;
  key: string;
  name: string;
  serverId: string | null;
  status: StepStatus;
  error?: string;
  startedAt: string | null;
  finishedAt: string | null;
}

export interface Job {
  id: string;
  kind: string;
  status: JobStatus;
  targetType: string;
  targetId: string;
  params: unknown;
  result?: unknown;
  error?: string;
  attempts: number;
  createdBy: string;
  createdAt: string;
  startedAt: string | null;
  finishedAt: string | null;
  updatedAt: string;
  steps?: JobStep[];
  lastLogSeq: number;
}

export interface LogLine {
  jobId: string;
  seq: number;
  ts: string;
  stream: "stdout" | "stderr" | "system";
  stepId?: number;
  serverId?: string;
  line: string;
}

export interface AuditEntry {
  id: number;
  ts: string;
  actor: string;
  action: string;
  targetType: string;
  targetId: string;
  details?: unknown;
  remoteAddr: string;
}

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message);
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`/api/v1${path}`, {
    method,
    credentials: "same-origin",
    headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (res.status === 204 || res.status === 202) {
    const text = await res.text();
    return (text ? JSON.parse(text) : undefined) as T;
  }
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    throw new ApiError(res.status, data.error ?? "error", data.message ?? res.statusText);
  }
  return data as T;
}

export const api = {
  login: (token: string) => request<{ user: string }>("POST", "/auth/login", { token }),
  logout: () => request<void>("POST", "/auth/logout"),
  me: () => request<{ user: string; version: string }>("GET", "/auth/me"),

  servers: () => request<Server[]>("GET", "/servers"),
  server: (id: string) => request<Server>("GET", `/servers/${id}`),
  createServer: (input: ServerInput) => request<Server>("POST", "/servers", input),
  updateServer: (id: string, input: ServerInput) => request<Server>("PATCH", `/servers/${id}`, input),
  deleteServer: (id: string) => request<void>("DELETE", `/servers/${id}`),
  hostKey: (id: string) =>
    request<{ hostPort: string; keyType: string; fingerprint: string; firstSeen: string; lastSeen: string }>(
      "GET",
      `/servers/${id}/host-key`,
    ),
  resetHostKey: (id: string) => request<void>("DELETE", `/servers/${id}/host-key`),
  preflight: (id: string) => request<Job>("POST", `/servers/${id}/preflight`),
  preflightMany: (serverIds: string[]) => request<Job>("POST", "/preflight", { serverIds }),

  jobs: (params?: { targetId?: string; limit?: number }) => {
    const q = new URLSearchParams();
    if (params?.targetId) q.set("targetId", params.targetId);
    if (params?.limit) q.set("limit", String(params.limit));
    return request<Job[]>("GET", `/jobs${q.size ? `?${q}` : ""}`);
  },
  job: (id: string) => request<Job>("GET", `/jobs/${id}`),
  cancelJob: (id: string) => request<void>("POST", `/jobs/${id}/cancel`),
  retryJob: (id: string) => request<void>("POST", `/jobs/${id}/retry`),

  audit: () => request<AuditEntry[]>("GET", "/audit"),
};

export const isFinal = (s: JobStatus) => s === "succeeded" || s === "failed" || s === "cancelled" || s === "paused";
