package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/efbecker/k8sfront/apps/api/internal/store"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestWALAndMigrationsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	ctx := context.Background()
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	mode, _ := db.JournalMode(ctx)
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
	db.Close()
	db, err = Open(ctx, path) // re-applying migrations must be a no-op
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
}

func TestServerLifecycleDeletesSecrets(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if err := db.PutSecret(ctx, &store.Secret{ID: "s1", Kind: store.SecretSSHPassword, Ciphertext: []byte{1}, KeyID: "k"}); err != nil {
		t.Fatal(err)
	}
	sid := "s1"
	srv := &store.Server{ID: "srv1", Name: "n", Host: "10.0.0.1", Port: 22, SSHUser: "debian", AuthMethod: store.AuthPassword, CredentialSecretID: &sid}
	if err := db.CreateServer(ctx, srv); err != nil {
		t.Fatal(err)
	}
	dup := *srv
	dup.ID = "srv2"
	dup.CredentialSecretID = nil
	if err := db.CreateServer(ctx, &dup); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate host:port must conflict, got %v", err)
	}
	if err := db.DeleteServer(ctx, "srv1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetSecret(ctx, "s1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("secret should be deleted with its server, got %v", err)
	}
}

func TestJobTransitionsAndLogs(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	j := &store.Job{ID: "j1", Kind: "k", Status: store.JobPending}
	if err := db.CreateJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionJob(ctx, "j1", []store.JobStatus{store.JobPending}, store.JobRunning, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionJob(ctx, "j1", []store.JobStatus{store.JobPending}, store.JobRunning, ""); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("invalid transition must conflict, got %v", err)
	}
	if err := db.AppendLogs(ctx, []store.LogLine{{JobID: "j1", Seq: 1, Stream: store.StreamStdout, Line: "a"}, {JobID: "j1", Seq: 2, Stream: store.StreamStdout, Line: "b"}}); err != nil {
		t.Fatal(err)
	}
	logs, _ := db.ListLogs(ctx, "j1", 1, 0)
	if len(logs) != 1 || logs[0].Line != "b" {
		t.Fatalf("resume after seq 1 should return only b: %+v", logs)
	}
	got, _ := db.GetJob(ctx, "j1")
	if got.Status != store.JobRunning || got.Attempts != 1 || got.StartedAt == nil || got.LastLogSeq != 2 {
		t.Fatalf("unexpected job: %+v", got)
	}
}
