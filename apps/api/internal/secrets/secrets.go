// Package secrets stores encrypted credentials using the vault and the store.
package secrets

import (
	"context"
	"fmt"

	"github.com/efbecker/k8sfront/apps/api/internal/store"
	"github.com/efbecker/k8sfront/apps/api/internal/vault"
	"github.com/google/uuid"
)

// Manager encrypts on write and decrypts on read. Plaintext is never persisted.
type Manager struct {
	st store.Store
	v  *vault.Vault
}

func NewManager(st store.Store, v *vault.Vault) *Manager { return &Manager{st: st, v: v} }

func aad(id string, kind store.SecretKind) []byte {
	return []byte("k8sfront:secret:" + id + ":" + string(kind))
}

// Put encrypts plaintext and returns the new secret ID.
func (m *Manager) Put(ctx context.Context, kind store.SecretKind, plaintext []byte) (string, error) {
	id := uuid.NewString()
	ct, err := m.v.Seal(plaintext, aad(id, kind))
	if err != nil {
		return "", err
	}
	if err := m.st.PutSecret(ctx, &store.Secret{ID: id, Kind: kind, Ciphertext: ct, KeyID: m.v.KeyID()}); err != nil {
		return "", fmt.Errorf("secrets: persist: %w", err)
	}
	return id, nil
}

// Get decrypts a secret. Callers should vault.Wipe the result when done.
func (m *Manager) Get(ctx context.Context, id string) ([]byte, error) {
	s, err := m.st.GetSecret(ctx, id)
	if err != nil {
		return nil, err
	}
	pt, err := m.v.Open(s.Ciphertext, aad(s.ID, s.Kind))
	if err != nil {
		return nil, fmt.Errorf("secrets: decrypt %s: %w", id, err)
	}
	return pt, nil
}

// GetOptional decrypts the secret when id is non-nil.
func (m *Manager) GetOptional(ctx context.Context, id *string) ([]byte, error) {
	if id == nil {
		return nil, nil
	}
	return m.Get(ctx, *id)
}

func (m *Manager) Delete(ctx context.Context, id string) error { return m.st.DeleteSecret(ctx, id) }
