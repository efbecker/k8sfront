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

/** How nodes reach the API: HAProxy on 127.0.0.1 in every node (default), kube-vip VIP, or the first CP (legacy). */
export type EndpointMode = "local-proxy" | "kube-vip" | "direct";

/** controlPlaneEndpoint name in local-proxy mode (resolves to 127.0.0.1 on every node). */
export const LOCAL_PROXY_ENDPOINT = "k8s-api.k8sfront.internal:7445";

export function endpointMode(o: ClusterOptions): EndpointMode {
  return o.endpointMode ?? (o.kubeVip?.address ? "kube-vip" : "direct");
}

export interface ClusterOptions {
  untaintControlPlane: boolean;
  metricsServer: boolean;
  endpointMode?: EndpointMode;
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
  /** Ready endpoint IPs, only for services without a selector (e.g. default/kubernetes). */
  endpointIPs?: string[];
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

// --- Deploy center (phase 4) ----------------------------------------------------

export interface AppSpec {
  name: string;
  namespace: string;
  image: string;
  replicas: number;
  containerPort: number;
  env: { name: string; value: string }[];
  healthPath: string;
  resources: { cpuRequest: string; memoryRequest: string; cpuLimit: string; memoryLimit: string };
  service?: { type: "ClusterIP" | "NodePort" | "LoadBalancer"; port: number; nodePort: number };
  ingress?: { host: string; path: string; className: string; tlsSecret: string };
  volume?: { size: string; storageClass: string; mountPath: string; accessMode: string };
  /** Manual credentials, an existing Secret (name), or a registered registry (registryId). */
  pullSecret?: { name: string; server: string; username: string; password: string; registryId?: string };
}

export interface ApplyResult {
  apiVersion: string;
  kind: string;
  namespace?: string;
  name: string;
  action: "created" | "configured" | "unchanged" | "deleted" | "absent" | "error";
  error?: string;
  note?: string;
}

export interface RemovalPod {
  namespace: string;
  name: string;
  ownerKind?: string;
  ownerName?: string;
  fate: "moves" | "daemon" | "blocks" | "done";
  emptyDir?: boolean;
}

export interface ServerRemoval {
  inCluster: boolean;
  clusterId?: string;
  clusterName?: string;
  role: string;
  blocked?: string;
  warnings: string[];
  node?: { nodeName: string; pods: RemovalPod[]; otherSchedulable: number };
}

export interface ObjectRef {
  apiVersion: string;
  kind: string;
  namespace?: string;
  name: string;
}

/** What to delete: a manifest, an explicit list, or a form app. */
export interface DeleteRequest {
  manifest?: string;
  objects?: ObjectRef[];
  app?: AppSpec;
  namespace?: string;
  keepNamespaces?: boolean;
  alsoNamespaces?: string[];
}

export interface NSItem {
  apiVersion: string;
  kind: string;
  name: string;
}

export interface NamespaceUsage {
  name: string;
  phase: string;
  created: string;
  system: boolean;
  pods: number;
  counts: Record<string, number>;
  items: NSItem[];
}

export interface ApplySummary {
  dryRun: boolean;
  results: ApplyResult[];
  failed: number;
}

export interface HelmRelease {
  name: string;
  namespace: string;
  revision: number;
  status: string;
  updated?: string;
}

export interface HelmInput {
  action: "install" | "uninstall";
  release: string;
  namespace: string;
  chart?: string;
  repo?: string;
  version?: string;
  values?: string;
}

// --- Updates & lifecycle (phase 5) ----------------------------------------------

export interface UpgradeCheck {
  id: string;
  name: string;
  status: CheckStatus;
  detail: string;
}

export interface UpgradeTarget {
  version: string;
  kind: "patch" | "minor";
  hops: string[];
  latest: boolean;
}

export interface NodeVersion {
  name: string;
  controlPlane: boolean;
  ready: boolean;
  unschedulable: boolean;
  kubeletVersion: string;
  internalIp: string;
  osImage: string;
  kernelVersion: string;
  containerRuntime: string;
}

export interface UpgradePlan {
  clusterVersion: string;
  serverVersion: string;
  nodes: NodeVersion[];
  checks: UpgradeCheck[];
  targets: UpgradeTarget[];
  blocked: boolean;
  pausedJob?: Job;
  inProgress?: boolean;
}

export interface OSPackage {
  name: string;
  suite: string;
  from: string;
  to: string;
  security: boolean;
  held: boolean;
}

export interface OSNodeReport {
  serverId: string;
  name: string;
  kernel: string;
  packages: OSPackage[];
  held: string[];
  security: number;
  rebootRequired: boolean;
  error?: string;
}

export interface CertInfo {
  name: string;
  expires: string;
  residual: string;
  authority: boolean;
  external: boolean;
}

export interface CertsResult {
  action: "check" | "renew";
  nodes: { serverId: string; name: string; certs: CertInfo[] }[];
  checkedAt: string;
}

export type RebootPolicy = "never" | "if-required" | "always";

// --- Registries & images ---------------------------------------------------------

export type RegistryKind = "dockerhub" | "ghcr" | "quay" | "harbor" | "generic";

export interface Registry {
  id: string;
  name: string;
  kind: RegistryKind;
  host: string;
  endpoint: string;
  mirrors: string[];
  username: string;
  hasPassword: boolean;
  caCert: string;
  skipVerify: boolean;
  pullSecret: boolean;
  /** Empty: every non-system namespace. */
  namespaces: string[];
  /** Empty: every managed cluster. */
  clusterIds: string[];
  secretName: string;
  createdAt: string;
  updatedAt: string;
}

export interface RegistryInput {
  name?: string;
  kind?: RegistryKind;
  host?: string;
  endpoint?: string;
  mirrors?: string[];
  username?: string;
  password?: string;
  clearPassword?: boolean;
  caCert?: string;
  skipVerify?: boolean;
  pullSecret?: boolean;
  namespaces?: string[];
  clusterIds?: string[];
}

export interface RegistryCheck {
  ok: boolean;
  authenticated: boolean;
  message: string;
  endpoint: string;
}

export interface CatalogImage {
  ref: string;
  name: string;
  host: string;
  tag?: string;
  digest?: string;
  sizeBytes: number;
  nodes: string[];
  pods: number;
  uses: { namespace: string; kind: string; name: string }[];
  inUse: boolean;
  system: boolean;
  registry?: string;
}

export interface ImageCatalog {
  images: CatalogImage[];
  nodes: string[];
  truncated: string[];
  collectedAt: string;
}

export interface PullState {
  node: string;
  image: string;
  state: "pending" | "pulled" | "failed";
  message?: string;
}

export interface PruneNode {
  serverId: string;
  name: string;
  removed: number;
  remaining: number;
  freedBytes: number;
  error?: string;
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
  serverRemoval: (id: string) => request<ServerRemoval>("GET", `/servers/${id}/removal`),
  /** Free server: {deleted:true}. Cluster worker: the graceful removal job. */
  removeServer: (id: string) => request<Job | { deleted: true }>("POST", `/servers/${id}/remove`),
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

  renderApp: (app: AppSpec) => request<{ yaml: string; objects: { kind: string; name: string; namespace?: string }[] }>("POST", "/deploy/render", app),
  deployApp: (id: string, app: AppSpec, dryRun: boolean) => request<ApplySummary>("POST", `/clusters/${id}/deploy/app`, { app, dryRun }),
  applyManifest: (id: string, manifest: string, namespace: string, dryRun: boolean) =>
    request<ApplySummary>("POST", `/clusters/${id}/apply`, { manifest, namespace, dryRun }),
  workloadRelated: (id: string, ns: string, kind: string, name: string) =>
    request<ObjectRef[]>("GET", `/clusters/${id}/k8s/workloads/${encodeURIComponent(ns)}/${kind}/${encodeURIComponent(name)}/related`),
  namespaceUsage: (id: string, ns?: string) =>
    request<NamespaceUsage[]>("GET", `/clusters/${id}/k8s/namespaces-usage${ns ? `/${encodeURIComponent(ns)}` : ""}`),
  deleteObjects: (id: string, req: DeleteRequest, dryRun: boolean) => request<ApplySummary>("POST", `/clusters/${id}/delete`, { ...req, dryRun }),
  helmReleases: (id: string) => request<HelmRelease[]>("GET", `/clusters/${id}/helm/releases`),
  helm: (id: string, input: HelmInput) => request<Job>("POST", `/clusters/${id}/helm`, input),

  upgradePlan: (id: string) => request<UpgradePlan>("GET", `/clusters/${id}/upgrades/plan`),
  startUpgrade: (id: string, targetVersion: string) => request<Job>("POST", `/clusters/${id}/upgrades`, { targetVersion }),
  addNode: (id: string, serverId: string, role: "control-plane" | "worker" = "worker") =>
    request<Job>("POST", `/clusters/${id}/nodes`, { serverId, role }),
  removeNode: (id: string, serverId: string) => request<Job>("DELETE", `/clusters/${id}/nodes/${serverId}`),
  cordon: (id: string, node: string) => request<void>("POST", `/clusters/${id}/k8s/nodes/${encodeURIComponent(node)}/cordon`),
  uncordon: (id: string, node: string) => request<void>("POST", `/clusters/${id}/k8s/nodes/${encodeURIComponent(node)}/uncordon`),
  drain: (id: string, node: string) => request<Job>("POST", `/clusters/${id}/k8s/nodes/${encodeURIComponent(node)}/drain`),
  osCheck: (id: string) => request<Job>("POST", `/clusters/${id}/os-updates/check`),
  osUpdate: (id: string, serverIds: string[], reboot: RebootPolicy) => request<Job>("POST", `/clusters/${id}/os-updates`, { serverIds, reboot }),
  rollingReboot: (id: string, serverIds: string[]) => request<Job>("POST", `/clusters/${id}/rolling-reboot`, { serverIds }),
  certs: (id: string, action: "check" | "renew") => request<Job>("POST", `/clusters/${id}/certs`, { action }),

  registries: () => request<Registry[]>("GET", "/registries"),
  createRegistry: (input: RegistryInput) => request<Registry>("POST", "/registries", input),
  updateRegistry: (id: string, input: RegistryInput) => request<Registry>("PATCH", `/registries/${id}`, input),
  deleteRegistry: (id: string) => request<void>("DELETE", `/registries/${id}`),
  checkRegistry: (id: string) => request<RegistryCheck>("POST", `/registries/${id}/check`),
  syncRegistries: (clusterId: string) => request<Job>("POST", `/clusters/${clusterId}/registries/sync`),
  images: (clusterId: string) => request<ImageCatalog>("GET", `/clusters/${clusterId}/k8s/images`),
  prepullImages: (clusterId: string, images: string[]) => request<Job>("POST", `/clusters/${clusterId}/images/prepull`, { images }),
  pruneImages: (clusterId: string, serverIds: string[] = []) => request<Job>("POST", `/clusters/${clusterId}/images/prune`, { serverIds }),
};

/** Human labels for job kinds. */
export const jobKindLabel: Record<string, string> = {
  "servers.preflight": "Teste de conexão & pré-requisitos",
  "cluster.bootstrap": "Bootstrap do cluster",
  "cluster.upgrade": "Upgrade do Kubernetes",
  "cluster.node-add": "Adicionar worker",
  "cluster.node-remove": "Remover worker",
  "cluster.node-drain": "Drenar nó",
  "cluster.os-check": "Verificar atualizações do SO",
  "cluster.os-update": "Atualizar SO (rolling)",
  "cluster.rolling-reboot": "Rolling reboot",
  "cluster.certs": "Certificados do kubeadm",
  "cluster.helm": "Helm",
  "cluster.registries-sync": "Aplicar registries",
  "cluster.image-prepull": "Pré-pull de imagens",
  "cluster.image-prune": "Limpar imagens sem uso",
};

export const isFinal = (s: JobStatus) => s === "succeeded" || s === "failed" || s === "cancelled" || s === "paused";
