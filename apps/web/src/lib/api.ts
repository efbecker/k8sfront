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

export type CNI = "cilium" | "calico";
export type Topology = "single-node" | "multi-node";
export type ClusterStatus = "draft" | "provisioning" | "ready" | "failed";

export interface K8sRelease {
  version: string;
  minor: string;
  latest: boolean;
}

export interface ClusterOptions {
  untaintControlPlane: boolean;
  metricsServer: boolean;
  kubeVip?: { address: string; interface?: string };
  cniVersion?: string;
  extraSans?: string[];
}

export interface Cluster {
  id: string;
  name: string;
  kubernetesVersion: string;
  podCidr: string;
  serviceCidr: string;
  cni: CNI;
  topology: Topology;
  controlPlaneEndpoint: string;
  status: ClusterStatus;
  options: ClusterOptions;
  apiServerUrl: string;
  hasKubeconfig: boolean;
  nodes: Server[];
  bootstrapJob?: Job;
  createdAt: string;
  updatedAt: string;
}

export interface ClusterInput {
  name: string;
  kubernetesVersion: string;
  podCidr: string;
  serviceCidr: string;
  cni: CNI;
  topology: Topology;
  options: ClusterOptions;
  nodes: { serverId: string; role: "control-plane" | "worker" }[];
  start: boolean;
}

// --- Kubernetes (phase 3) -------------------------------------------------------

export interface K8sNode {
  uid: string;
  name: string;
  roles: string[] | null;
  ready: boolean;
  unschedulable: boolean;
  conditions?: string[];
  kubeletVersion: string;
  internalIP: string;
  osImage: string;
  kernelVersion: string;
  containerRuntime: string;
  architecture: string;
  podCIDR?: string;
  cpuMillis: number;
  memoryBytes: number;
  podsCapacity: number;
  taints?: string[];
  labels?: Record<string, string>;
  created: string;
}

export interface K8sPod {
  uid: string;
  name: string;
  namespace: string;
  node: string;
  phase: string;
  reason?: string;
  ip?: string;
  ready: number;
  containers: number;
  restarts: number;
  images: string[] | null;
  ownerKind?: string;
  ownerName?: string;
  labels?: Record<string, string>;
  qosClass?: string;
  created: string;
}

export interface K8sService {
  uid: string;
  name: string;
  namespace: string;
  type: string;
  clusterIP: string;
  externalIPs?: string[];
  ports: string[];
  selector?: Record<string, string>;
  created: string;
}

export interface NodeUsage extends K8sNode {
  pods: number;
  cpuRequestedMillis: number;
  memoryRequestedBytes: number;
  cpuUsedMillis: number | null;
  memoryUsedBytes: number | null;
}

export interface ResourceTotals {
  allocatable: number;
  requested: number;
  used: number | null;
}

export interface K8sWorkload {
  kind: "Deployment" | "StatefulSet" | "DaemonSet";
  name: string;
  namespace: string;
  desired: number;
  ready: number;
  updated: number;
  available: number;
  images: string[];
  created: string;
}

export interface K8sEvent {
  type: string;
  reason: string;
  message: string;
  kind: string;
  name: string;
  namespace: string;
  count: number;
  last: string;
}

export interface ClusterOverview {
  version: string;
  platform: string;
  nodes: NodeUsage[];
  nodesReady: number;
  pods: { total: number; running: number; pending: number; succeeded: number; failed: number; problem: number };
  namespaces: number;
  services: number;
  deployments: number;
  deploymentsReady: number;
  cpu: ResourceTotals;
  memory: ResourceTotals;
  metricsAvailable: boolean;
  controlPlane: { name: string; node: string; ready: boolean; restarts: number }[];
  system: K8sWorkload[];
  events: K8sEvent[];
  collectedAt: string;
}

export interface WorkloadList {
  workloads: K8sWorkload[];
  pods: K8sPod[];
}

export interface NetworkList {
  services: K8sService[];
  ingresses: { name: string; namespace: string; class: string; hosts: string[]; address: string[]; tls: boolean; created: string }[];
  networkPolicies: number;
  pvcs: { name: string; namespace: string; status: string; volume: string; capacity: string; storageClass: string; accessModes: string[]; created: string }[];
  pvs: { name: string; status: string; capacity: string; claim: string; storageClass: string; reclaimPolicy: string; created: string }[];
  storageClasses: { name: string; provisioner: string; default: boolean; reclaimPolicy: string; bindingMode: string }[];
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

  kubernetesVersions: () => request<K8sRelease[]>("GET", "/kubernetes/versions"),
  clusters: () => request<Cluster[]>("GET", "/clusters"),
  cluster: (id: string) => request<Cluster>("GET", `/clusters/${id}`),
  createCluster: (input: ClusterInput) => request<Cluster>("POST", "/clusters", input),
  deleteCluster: (id: string) => request<void>("DELETE", `/clusters/${id}`),
  bootstrapCluster: (id: string) => request<Job>("POST", `/clusters/${id}/bootstrap`),
  kubeconfigUrl: (id: string) => `/api/v1/clusters/${id}/kubeconfig`,

  overview: (id: string) => request<ClusterOverview>("GET", `/clusters/${id}/k8s/overview`),
  namespaces: (id: string) => request<string[]>("GET", `/clusters/${id}/k8s/namespaces`),
  workloads: (id: string, ns?: string) =>
    request<WorkloadList>("GET", `/clusters/${id}/k8s/workloads${ns ? `?namespace=${encodeURIComponent(ns)}` : ""}`),
  network: (id: string, ns?: string) =>
    request<NetworkList>("GET", `/clusters/${id}/k8s/network${ns ? `?namespace=${encodeURIComponent(ns)}` : ""}`),
};

export const isFinal = (s: JobStatus) => s === "succeeded" || s === "failed" || s === "cancelled" || s === "paused";
