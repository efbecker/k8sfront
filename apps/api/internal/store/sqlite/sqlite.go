// Package sqlite implements store.Store on SQLite in WAL mode using the
// pure-Go modernc.org/sqlite driver (no CGO).
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/store"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB is the SQLite-backed store.
type DB struct {
	db *sql.DB
}

var _ store.Store = (*DB)(nil)

// Open opens (creating if needed) the database file and applies migrations.
func Open(ctx context.Context, path string) (*DB, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("sqlite: create data dir: %w", err)
		}
	}
	q := url.Values{}
	for _, p := range []string{"journal_mode(WAL)", "busy_timeout(10000)", "foreign_keys(1)", "synchronous(NORMAL)"} {
		q.Add("_pragma", p)
	}
	q.Set("_txlock", "immediate")
	dsn := "file:" + path + "?" + q.Encode()
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}
	if path == ":memory:" {
		sdb.SetMaxOpenConns(1)
	} else {
		sdb.SetMaxOpenConns(8)
	}
	d := &DB{db: sdb}
	if err := d.ping(ctx); err != nil {
		sdb.Close()
		return nil, err
	}
	if err := d.migrate(ctx); err != nil {
		sdb.Close()
		return nil, err
	}
	if path != ":memory:" {
		_ = os.Chmod(path, 0o600)
	}
	return d, nil
}

func (d *DB) ping(ctx context.Context) error {
	var mode string
	if err := d.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return fmt.Errorf("sqlite: ping: %w", err)
	}
	return nil
}

// JournalMode reports the active journal mode (expected "wal").
func (d *DB) JournalMode(ctx context.Context) (string, error) {
	var mode string
	err := d.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode)
	return mode, err
}

func (d *DB) Close() error { return d.db.Close() }

func (d *DB) migrate(ctx context.Context) error {
	if _, err := d.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("sqlite: migrations table: %w", err)
	}
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		version, err := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if err != nil {
			return fmt.Errorf("sqlite: bad migration name %q", e.Name())
		}
		var exists int
		if err := d.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM schema_migrations WHERE version = ?`, version).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		tx, err := d.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("sqlite: migration %s: %w", e.Name(), err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, version, ts(time.Now())); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// --- helpers -----------------------------------------------------------------

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func tsPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ts(*t)
}

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func parseTSPtr(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	t := parseTS(ns.String)
	return &t
}

func strPtr(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	s := ns.String
	return &s
}

func rawJSON(ns sql.NullString) json.RawMessage {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	return json.RawMessage(ns.String)
}

func nullJSON(m json.RawMessage) any {
	if len(m) == 0 {
		return nil
	}
	return string(m)
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	return err
}

type scanner interface{ Scan(dest ...any) error }

// --- secrets -------------------------------------------------------------------

func (d *DB) PutSecret(ctx context.Context, s *store.Secret) error {
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now()
	}
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO secrets(id, kind, ciphertext, key_id, created_at) VALUES (?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET kind=excluded.kind, ciphertext=excluded.ciphertext, key_id=excluded.key_id`,
		s.ID, string(s.Kind), s.Ciphertext, s.KeyID, ts(s.CreatedAt))
	return err
}

func (d *DB) GetSecret(ctx context.Context, id string) (*store.Secret, error) {
	var s store.Secret
	var kind, created string
	err := d.db.QueryRowContext(ctx, `SELECT id, kind, ciphertext, key_id, created_at FROM secrets WHERE id = ?`, id).
		Scan(&s.ID, &kind, &s.Ciphertext, &s.KeyID, &created)
	if err != nil {
		return nil, notFound(err)
	}
	s.Kind = store.SecretKind(kind)
	s.CreatedAt = parseTS(created)
	return &s, nil
}

func (d *DB) DeleteSecret(ctx context.Context, id string) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, id)
	return err
}

// --- servers -------------------------------------------------------------------

const serverCols = `id, cluster_id, name, host, port, ssh_user, auth_method, role,
	credential_secret_id, passphrase_secret_id, sudo_secret_id, sudo_mode,
	os_id, os_version_id, os_pretty_name, kernel,
	last_check_at, last_check_status, last_check_report, created_at, updated_at`

func scanServer(r scanner) (*store.Server, error) {
	var s store.Server
	var clusterID, credID, passID, sudoID, lastAt, report sql.NullString
	var auth, role, sudo, created, updated string
	err := r.Scan(&s.ID, &clusterID, &s.Name, &s.Host, &s.Port, &s.SSHUser, &auth, &role,
		&credID, &passID, &sudoID, &sudo, &s.OSID, &s.OSVersionID, &s.OSPrettyName, &s.Kernel,
		&lastAt, &s.LastCheckStatus, &report, &created, &updated)
	if err != nil {
		return nil, err
	}
	s.ClusterID, s.CredentialSecretID, s.PassphraseSecretID, s.SudoSecretID = strPtr(clusterID), strPtr(credID), strPtr(passID), strPtr(sudoID)
	s.AuthMethod, s.Role, s.SudoMode = store.AuthMethod(auth), store.NodeRole(role), store.SudoMode(sudo)
	s.LastCheckAt = parseTSPtr(lastAt)
	s.LastCheckReport = rawJSON(report)
	s.CreatedAt, s.UpdatedAt = parseTS(created), parseTS(updated)
	return &s, nil
}

func (d *DB) CreateServer(ctx context.Context, s *store.Server) error {
	now := time.Now()
	s.CreatedAt, s.UpdatedAt = now, now
	if s.SudoMode == "" {
		s.SudoMode = store.SudoUnknown
	}
	_, err := d.db.ExecContext(ctx, `INSERT INTO servers(`+serverCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.ID, s.ClusterID, s.Name, s.Host, s.Port, s.SSHUser, string(s.AuthMethod), string(s.Role),
		s.CredentialSecretID, s.PassphraseSecretID, s.SudoSecretID, string(s.SudoMode),
		s.OSID, s.OSVersionID, s.OSPrettyName, s.Kernel,
		tsPtr(s.LastCheckAt), s.LastCheckStatus, nullJSON(s.LastCheckReport), ts(s.CreatedAt), ts(s.UpdatedAt))
	if isUnique(err) {
		return fmt.Errorf("%w: a server with host %s:%d already exists", store.ErrConflict, s.Host, s.Port)
	}
	return err
}

func (d *DB) UpdateServer(ctx context.Context, s *store.Server) error {
	s.UpdatedAt = time.Now()
	res, err := d.db.ExecContext(ctx, `UPDATE servers SET cluster_id=?, name=?, host=?, port=?, ssh_user=?, auth_method=?, role=?,
		credential_secret_id=?, passphrase_secret_id=?, sudo_secret_id=?, sudo_mode=?,
		os_id=?, os_version_id=?, os_pretty_name=?, kernel=?,
		last_check_at=?, last_check_status=?, last_check_report=?, updated_at=? WHERE id=?`,
		s.ClusterID, s.Name, s.Host, s.Port, s.SSHUser, string(s.AuthMethod), string(s.Role),
		s.CredentialSecretID, s.PassphraseSecretID, s.SudoSecretID, string(s.SudoMode),
		s.OSID, s.OSVersionID, s.OSPrettyName, s.Kernel,
		tsPtr(s.LastCheckAt), s.LastCheckStatus, nullJSON(s.LastCheckReport), ts(s.UpdatedAt), s.ID)
	if isUnique(err) {
		return fmt.Errorf("%w: a server with host %s:%d already exists", store.ErrConflict, s.Host, s.Port)
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (d *DB) GetServer(ctx context.Context, id string) (*store.Server, error) {
	s, err := scanServer(d.db.QueryRowContext(ctx, `SELECT `+serverCols+` FROM servers WHERE id = ?`, id))
	return s, notFound(err)
}

func (d *DB) ListServers(ctx context.Context) ([]store.Server, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+serverCols+` FROM servers ORDER BY name, host`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.Server{}
	for rows.Next() {
		s, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// DeleteServer removes the server and its secrets in one transaction.
func (d *DB) DeleteServer(ctx context.Context, id string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var cred, pass, sudo sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT credential_secret_id, passphrase_secret_id, sudo_secret_id FROM servers WHERE id = ?`, id).
		Scan(&cred, &pass, &sudo); err != nil {
		return notFound(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM servers WHERE id = ?`, id); err != nil {
		return err
	}
	for _, sid := range []sql.NullString{cred, pass, sudo} {
		if sid.Valid {
			if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, sid.String); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// --- known hosts -----------------------------------------------------------------

func (d *DB) GetKnownHost(ctx context.Context, hostPort string) (*store.KnownHost, error) {
	var k store.KnownHost
	var first, last string
	err := d.db.QueryRowContext(ctx, `SELECT host_port, key_type, public_key, fingerprint, first_seen, last_seen FROM known_hosts WHERE host_port = ?`, hostPort).
		Scan(&k.HostPort, &k.KeyType, &k.PublicKey, &k.Fingerprint, &first, &last)
	if err != nil {
		return nil, notFound(err)
	}
	k.FirstSeen, k.LastSeen = parseTS(first), parseTS(last)
	return &k, nil
}

func (d *DB) UpsertKnownHost(ctx context.Context, k *store.KnownHost) error {
	now := time.Now()
	if k.FirstSeen.IsZero() {
		k.FirstSeen = now
	}
	k.LastSeen = now
	_, err := d.db.ExecContext(ctx, `INSERT INTO known_hosts(host_port, key_type, public_key, fingerprint, first_seen, last_seen)
		VALUES (?,?,?,?,?,?) ON CONFLICT(host_port) DO UPDATE SET last_seen = excluded.last_seen
		WHERE known_hosts.fingerprint = excluded.fingerprint`,
		k.HostPort, k.KeyType, k.PublicKey, k.Fingerprint, ts(k.FirstSeen), ts(k.LastSeen))
	return err
}

func (d *DB) DeleteKnownHost(ctx context.Context, hostPort string) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM known_hosts WHERE host_port = ?`, hostPort)
	return err
}

// --- clusters ------------------------------------------------------------------

func (d *DB) CreateCluster(ctx context.Context, c *store.Cluster) error {
	now := time.Now()
	c.CreatedAt, c.UpdatedAt = now, now
	if c.Status == "" {
		c.Status = "draft"
	}
	_, err := d.db.ExecContext(ctx, `INSERT INTO clusters(id, name, kubernetes_version, pod_cidr, service_cidr, cni, topology,
		control_plane_endpoint, status, kubeconfig_secret_id, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.Name, c.KubernetesVersion, c.PodCIDR, c.ServiceCIDR, c.CNI, c.Topology,
		c.ControlPlaneEndpoint, c.Status, c.KubeconfigSecretID, ts(c.CreatedAt), ts(c.UpdatedAt))
	if isUnique(err) {
		return fmt.Errorf("%w: cluster %q already exists", store.ErrConflict, c.Name)
	}
	return err
}

func (d *DB) ListClusters(ctx context.Context) ([]store.Cluster, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id, name, kubernetes_version, pod_cidr, service_cidr, cni, topology,
		control_plane_endpoint, status, kubeconfig_secret_id, created_at, updated_at FROM clusters ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.Cluster{}
	for rows.Next() {
		var c store.Cluster
		var kc sql.NullString
		var created, updated string
		if err := rows.Scan(&c.ID, &c.Name, &c.KubernetesVersion, &c.PodCIDR, &c.ServiceCIDR, &c.CNI, &c.Topology,
			&c.ControlPlaneEndpoint, &c.Status, &kc, &created, &updated); err != nil {
			return nil, err
		}
		c.KubeconfigSecretID = strPtr(kc)
		c.CreatedAt, c.UpdatedAt = parseTS(created), parseTS(updated)
		out = append(out, c)
	}
	return out, rows.Err()
}

// --- jobs ------------------------------------------------------------------------

const jobCols = `id, kind, status, target_type, target_id, params, result, error, attempts, created_by,
	created_at, started_at, finished_at, updated_at`

func scanJob(r scanner) (*store.Job, error) {
	var j store.Job
	var status, params, created, updated string
	var result, started, finished sql.NullString
	if err := r.Scan(&j.ID, &j.Kind, &status, &j.TargetType, &j.TargetID, &params, &result, &j.Error, &j.Attempts,
		&j.CreatedBy, &created, &started, &finished, &updated); err != nil {
		return nil, err
	}
	j.Status = store.JobStatus(status)
	j.Params = json.RawMessage(params)
	j.Result = rawJSON(result)
	j.CreatedAt, j.UpdatedAt = parseTS(created), parseTS(updated)
	j.StartedAt, j.FinishedAt = parseTSPtr(started), parseTSPtr(finished)
	return &j, nil
}

func (d *DB) CreateJob(ctx context.Context, j *store.Job) error {
	now := time.Now()
	j.CreatedAt, j.UpdatedAt = now, now
	if len(j.Params) == 0 {
		j.Params = json.RawMessage("{}")
	}
	_, err := d.db.ExecContext(ctx, `INSERT INTO jobs(`+jobCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		j.ID, j.Kind, string(j.Status), j.TargetType, j.TargetID, string(j.Params), nullJSON(j.Result), j.Error, j.Attempts,
		j.CreatedBy, ts(j.CreatedAt), tsPtr(j.StartedAt), tsPtr(j.FinishedAt), ts(j.UpdatedAt))
	return err
}

func (d *DB) GetJob(ctx context.Context, id string) (*store.Job, error) {
	j, err := scanJob(d.db.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE id = ?`, id))
	if err != nil {
		return nil, notFound(err)
	}
	if j.Steps, err = d.ListSteps(ctx, id); err != nil {
		return nil, err
	}
	if j.LastLogSeq, err = d.MaxLogSeq(ctx, id); err != nil {
		return nil, err
	}
	return j, nil
}

func (d *DB) ListJobs(ctx context.Context, f store.JobFilter) ([]store.Job, error) {
	where, args := []string{"1=1"}, []any{}
	if len(f.Status) > 0 {
		ph := make([]string, len(f.Status))
		for i, s := range f.Status {
			ph[i] = "?"
			args = append(args, string(s))
		}
		where = append(where, "status IN ("+strings.Join(ph, ",")+")")
	}
	if f.TargetID != "" {
		where = append(where, "target_id = ?")
		args = append(args, f.TargetID)
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	args = append(args, limit)
	rows, err := d.db.QueryContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE `+strings.Join(where, " AND ")+` ORDER BY created_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

func (d *DB) TransitionJob(ctx context.Context, id string, from []store.JobStatus, to store.JobStatus, errMsg string) error {
	now := ts(time.Now())
	ph := make([]string, len(from))
	args := []any{string(to), errMsg, now}
	set := "status = ?, error = ?, updated_at = ?"
	switch to {
	case store.JobRunning:
		set += ", started_at = COALESCE(started_at, ?), finished_at = NULL, attempts = attempts + 1"
		args = append(args, now)
	case store.JobSucceeded, store.JobFailed, store.JobCancelled, store.JobPaused:
		set += ", finished_at = ?"
		args = append(args, now)
	case store.JobPending:
		set += ", finished_at = NULL"
	}
	args = append(args, id)
	for i, s := range from {
		ph[i] = "?"
		args = append(args, string(s))
	}
	res, err := d.db.ExecContext(ctx, `UPDATE jobs SET `+set+` WHERE id = ? AND status IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := d.GetJob(ctx, id); err != nil {
			return err
		}
		return fmt.Errorf("%w: job %s cannot move to %s from its current state", store.ErrConflict, id, to)
	}
	return nil
}

func (d *DB) SetJobResult(ctx context.Context, id string, result json.RawMessage) error {
	_, err := d.db.ExecContext(ctx, `UPDATE jobs SET result = ?, updated_at = ? WHERE id = ?`, nullJSON(result), ts(time.Now()), id)
	return err
}

// --- steps ------------------------------------------------------------------------

func (d *DB) CreateStep(ctx context.Context, st *store.JobStep) error {
	res, err := d.db.ExecContext(ctx, `INSERT INTO job_steps(job_id, seq, key, name, server_id, status, error, started_at, finished_at)
		VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM job_steps WHERE job_id = ?), ?,?,?,?,?,?,?)`,
		st.JobID, st.JobID, st.Key, st.Name, st.ServerID, string(st.Status), st.Error, tsPtr(st.StartedAt), tsPtr(st.FinishedAt))
	if err != nil {
		return err
	}
	if st.ID, err = res.LastInsertId(); err != nil {
		return err
	}
	return d.db.QueryRowContext(ctx, `SELECT seq FROM job_steps WHERE id = ?`, st.ID).Scan(&st.Seq)
}

func (d *DB) UpdateStep(ctx context.Context, st *store.JobStep) error {
	_, err := d.db.ExecContext(ctx, `UPDATE job_steps SET status = ?, error = ?, started_at = ?, finished_at = ? WHERE id = ?`,
		string(st.Status), st.Error, tsPtr(st.StartedAt), tsPtr(st.FinishedAt), st.ID)
	return err
}

func (d *DB) ListSteps(ctx context.Context, jobID string) ([]store.JobStep, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id, job_id, seq, key, name, server_id, status, error, started_at, finished_at
		FROM job_steps WHERE job_id = ? ORDER BY seq`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.JobStep{}
	for rows.Next() {
		var st store.JobStep
		var server, started, finished sql.NullString
		var status string
		if err := rows.Scan(&st.ID, &st.JobID, &st.Seq, &st.Key, &st.Name, &server, &status, &st.Error, &started, &finished); err != nil {
			return nil, err
		}
		st.ServerID, st.Status = strPtr(server), store.StepStatus(status)
		st.StartedAt, st.FinishedAt = parseTSPtr(started), parseTSPtr(finished)
		out = append(out, st)
	}
	return out, rows.Err()
}

// --- logs -------------------------------------------------------------------------

func (d *DB) AppendLogs(ctx context.Context, lines []store.LogLine) error {
	if len(lines) == 0 {
		return nil
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO job_logs(job_id, seq, ts, stream, step_id, server_id, line) VALUES (?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, l := range lines {
		if _, err := stmt.ExecContext(ctx, l.JobID, l.Seq, ts(l.TS), string(l.Stream), l.StepID, l.ServerID, l.Line); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) ListLogs(ctx context.Context, jobID string, afterSeq int64, limit int) ([]store.LogLine, error) {
	if limit <= 0 || limit > 10000 {
		limit = 5000
	}
	rows, err := d.db.QueryContext(ctx, `SELECT job_id, seq, ts, stream, step_id, server_id, line FROM job_logs
		WHERE job_id = ? AND seq > ? ORDER BY seq LIMIT ?`, jobID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.LogLine{}
	for rows.Next() {
		var l store.LogLine
		var tsv, stream string
		var step sql.NullInt64
		var server sql.NullString
		if err := rows.Scan(&l.JobID, &l.Seq, &tsv, &stream, &step, &server, &l.Line); err != nil {
			return nil, err
		}
		l.TS, l.Stream, l.ServerID = parseTS(tsv), store.LogStream(stream), strPtr(server)
		if step.Valid {
			v := step.Int64
			l.StepID = &v
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (d *DB) MaxLogSeq(ctx context.Context, jobID string) (int64, error) {
	var n sql.NullInt64
	err := d.db.QueryRowContext(ctx, `SELECT MAX(seq) FROM job_logs WHERE job_id = ?`, jobID).Scan(&n)
	return n.Int64, err
}

// --- audit ------------------------------------------------------------------------

func (d *DB) AppendAudit(ctx context.Context, e *store.AuditEntry) error {
	if e.TS.IsZero() {
		e.TS = time.Now()
	}
	res, err := d.db.ExecContext(ctx, `INSERT INTO audit_log(ts, actor, action, target_type, target_id, details, remote_addr) VALUES (?,?,?,?,?,?,?)`,
		ts(e.TS), e.Actor, e.Action, e.TargetType, e.TargetID, nullJSON(e.Details), e.RemoteAddr)
	if err != nil {
		return err
	}
	e.ID, err = res.LastInsertId()
	return err
}

func (d *DB) ListAudit(ctx context.Context, limit int) ([]store.AuditEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := d.db.QueryContext(ctx, `SELECT id, ts, actor, action, target_type, target_id, details, remote_addr
		FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.AuditEntry{}
	for rows.Next() {
		var e store.AuditEntry
		var tsv string
		var details sql.NullString
		if err := rows.Scan(&e.ID, &tsv, &e.Actor, &e.Action, &e.TargetType, &e.TargetID, &details, &e.RemoteAddr); err != nil {
			return nil, err
		}
		e.TS, e.Details = parseTS(tsv), rawJSON(details)
		out = append(out, e)
	}
	return out, rows.Err()
}
