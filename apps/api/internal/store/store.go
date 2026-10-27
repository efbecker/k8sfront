// Package store defines the persistence model and the repository interface.
// The sqlite subpackage is the default implementation; a PostgreSQL
// implementation only has to satisfy Store.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("store: not found")
	ErrConflict = errors.New("store: conflict")
)

// AuthMethod is how the platform authenticates over SSH.
type AuthMethod string

const (
	AuthKey      AuthMethod = "key"
	AuthPassword AuthMethod = "password"
)

// NodeRole is the Kubernetes role a server will play.
type NodeRole string

const (
	RoleNone         NodeRole = ""
	RoleControlPlane NodeRole = "control-plane"
	RoleWorker       NodeRole = "worker"
)

// SudoMode is the privilege escalation strategy detected for a server.
type SudoMode string

const (
	SudoUnknown  SudoMode = "unknown"
	SudoRoot     SudoMode = "root"     // logged in as root, no sudo needed
	SudoNoPasswd SudoMode = "nopasswd" // sudo -n true succeeds
	SudoPassword SudoMode = "password" // sudo -S with password on stdin
)

// SecretKind classifies encrypted secrets. Kind is part of the AAD.
type SecretKind string

const (
	SecretSSHPrivateKey SecretKind = "ssh_private_key"
	SecretSSHPassphrase SecretKind = "ssh_key_passphrase"
	SecretSSHPassword   SecretKind = "ssh_password"
	SecretSudoPassword  SecretKind = "sudo_password"
	SecretKubeconfig    SecretKind = "kubeconfig"
)

// Secret is an encrypted blob. Plaintext never lives in this struct.
type Secret struct {
	ID         string
	Kind       SecretKind
	Ciphertext []byte
	KeyID      string
	CreatedAt  time.Time
}

// Server is a managed Debian 13+ host.
type Server struct {
	ID                 string          `json:"id"`
	ClusterID          *string         `json:"clusterId"`
	Name               string          `json:"name"`
	Host               string          `json:"host"`
	Port               int             `json:"port"`
	SSHUser            string          `json:"sshUser"`
	AuthMethod         AuthMethod      `json:"authMethod"`
	Role               NodeRole        `json:"role"`
	CredentialSecretID *string         `json:"-"`
	PassphraseSecretID *string         `json:"-"`
	SudoSecretID       *string         `json:"-"`
	SudoMode           SudoMode        `json:"sudoMode"`
	OSID               string          `json:"osId"`
	OSVersionID        string          `json:"osVersionId"`
	OSPrettyName       string          `json:"osPrettyName"`
	Kernel             string          `json:"kernel"`
	LastCheckAt        *time.Time      `json:"lastCheckAt"`
	LastCheckStatus    string          `json:"lastCheckStatus"`
	LastCheckReport    json.RawMessage `json:"lastCheckReport,omitempty"`
	CreatedAt          time.Time       `json:"createdAt"`
	UpdatedAt          time.Time       `json:"updatedAt"`
}

// KnownHost is a registered SSH host key (trust on first use, or pinned).
type KnownHost struct {
	HostPort    string    `json:"hostPort"`
	KeyType     string    `json:"keyType"`
	PublicKey   []byte    `json:"-"`
	Fingerprint string    `json:"fingerprint"`
	FirstSeen   time.Time `json:"firstSeen"`
	LastSeen    time.Time `json:"lastSeen"`
}

// Cluster is a Kubernetes cluster managed by the platform (used from phase 2).
type Cluster struct {
	ID                   string    `json:"id"`
	Name                 string    `json:"name"`
	KubernetesVersion    string    `json:"kubernetesVersion"`
	PodCIDR              string    `json:"podCidr"`
	ServiceCIDR          string    `json:"serviceCidr"`
	CNI                  string    `json:"cni"`
	Topology             string    `json:"topology"`
	ControlPlaneEndpoint string    `json:"controlPlaneEndpoint"`
	Status               string    `json:"status"`
	KubeconfigSecretID   *string   `json:"-"`
	CreatedAt            time.Time `json:"createdAt"`
	UpdatedAt            time.Time `json:"updatedAt"`
}

// JobStatus is a state in the job state machine (see ARCHITECTURE.md).
type JobStatus string

const (
	JobPending   JobStatus = "pending"
	JobRunning   JobStatus = "running"
	JobSucceeded JobStatus = "succeeded"
	JobFailed    JobStatus = "failed"
	JobPaused    JobStatus = "paused"
	JobCancelled JobStatus = "cancelled"
)

// Terminal reports whether no further automatic transition happens.
func (s JobStatus) Terminal() bool {
	return s == JobSucceeded || s == JobCancelled
}

// Job is a long-running background operation.
type Job struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	Status     JobStatus       `json:"status"`
	TargetType string          `json:"targetType"`
	TargetID   string          `json:"targetId"`
	Params     json.RawMessage `json:"params"`
	Result     json.RawMessage `json:"result,omitempty"`
	Error      string          `json:"error,omitempty"`
	Attempts   int             `json:"attempts"`
	CreatedBy  string          `json:"createdBy"`
	CreatedAt  time.Time       `json:"createdAt"`
	StartedAt  *time.Time      `json:"startedAt"`
	FinishedAt *time.Time      `json:"finishedAt"`
	UpdatedAt  time.Time       `json:"updatedAt"`
	Steps      []JobStep       `json:"steps,omitempty"`
	LastLogSeq int64           `json:"lastLogSeq"`
}

// StepStatus is the state of one step inside a job.
type StepStatus string

const (
	StepPending   StepStatus = "pending"
	StepRunning   StepStatus = "running"
	StepSucceeded StepStatus = "succeeded"
	StepFailed    StepStatus = "failed"
	StepSkipped   StepStatus = "skipped"
)

// JobStep is a unit of progress, optionally scoped to a server.
type JobStep struct {
	ID         int64      `json:"id"`
	JobID      string     `json:"jobId"`
	Seq        int        `json:"seq"`
	Key        string     `json:"key"`
	Name       string     `json:"name"`
	ServerID   *string    `json:"serverId"`
	Status     StepStatus `json:"status"`
	Error      string     `json:"error,omitempty"`
	StartedAt  *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
}

// LogStream classifies a log line.
type LogStream string

const (
	StreamStdout LogStream = "stdout"
	StreamStderr LogStream = "stderr"
	StreamSystem LogStream = "system"
)

// LogLine is one redacted, persisted line of job output.
type LogLine struct {
	JobID    string    `json:"jobId"`
	Seq      int64     `json:"seq"`
	TS       time.Time `json:"ts"`
	Stream   LogStream `json:"stream"`
	StepID   *int64    `json:"stepId,omitempty"`
	ServerID *string   `json:"serverId,omitempty"`
	Line     string    `json:"line"`
}

// AuditEntry records who did what.
type AuditEntry struct {
	ID         int64           `json:"id"`
	TS         time.Time       `json:"ts"`
	Actor      string          `json:"actor"`
	Action     string          `json:"action"`
	TargetType string          `json:"targetType"`
	TargetID   string          `json:"targetId"`
	Details    json.RawMessage `json:"details,omitempty"`
	RemoteAddr string          `json:"remoteAddr"`
}

// JobFilter narrows ListJobs.
type JobFilter struct {
	Status   []JobStatus
	TargetID string
	Limit    int
}

// Store is the full persistence contract.
type Store interface {
	Close() error

	PutSecret(ctx context.Context, s *Secret) error
	GetSecret(ctx context.Context, id string) (*Secret, error)
	DeleteSecret(ctx context.Context, id string) error

	CreateServer(ctx context.Context, s *Server) error
	UpdateServer(ctx context.Context, s *Server) error
	GetServer(ctx context.Context, id string) (*Server, error)
	ListServers(ctx context.Context) ([]Server, error)
	DeleteServer(ctx context.Context, id string) error

	GetKnownHost(ctx context.Context, hostPort string) (*KnownHost, error)
	UpsertKnownHost(ctx context.Context, kh *KnownHost) error
	DeleteKnownHost(ctx context.Context, hostPort string) error

	CreateCluster(ctx context.Context, c *Cluster) error
	ListClusters(ctx context.Context) ([]Cluster, error)

	CreateJob(ctx context.Context, j *Job) error
	GetJob(ctx context.Context, id string) (*Job, error)
	ListJobs(ctx context.Context, f JobFilter) ([]Job, error)
	// TransitionJob atomically moves a job from one of `from` to `to`.
	TransitionJob(ctx context.Context, id string, from []JobStatus, to JobStatus, errMsg string) error
	SetJobResult(ctx context.Context, id string, result json.RawMessage) error

	CreateStep(ctx context.Context, st *JobStep) error
	UpdateStep(ctx context.Context, st *JobStep) error
	ListSteps(ctx context.Context, jobID string) ([]JobStep, error)

	AppendLogs(ctx context.Context, lines []LogLine) error
	ListLogs(ctx context.Context, jobID string, afterSeq int64, limit int) ([]LogLine, error)
	MaxLogSeq(ctx context.Context, jobID string) (int64, error)

	AppendAudit(ctx context.Context, e *AuditEntry) error
	ListAudit(ctx context.Context, limit int) ([]AuditEntry, error)
}
