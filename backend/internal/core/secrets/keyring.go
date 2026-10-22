package secrets

import (
	"context"
	"crypto/rand"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Keyring holds one data encryption key (DEK) per tenant, wrapped by the master key.
// Secrets for a tenant are sealed with its DEK, so deleting the tenant's key row
// crypto-shreds them (GDPR erasure) and master-key rotation only rewraps DEKs.
type Keyring struct {
	master *Sealer
	pool   *pgxpool.Pool
	mu     sync.RWMutex
	cache  map[uuid.UUID]*Sealer
}

// NewKeyring creates a keyring.
func NewKeyring(master *Sealer, pool *pgxpool.Pool) *Keyring {
	return &Keyring{master: master, pool: pool, cache: map[uuid.UUID]*Sealer{}}
}

// For returns the tenant's sealer, creating its DEK on first use.
func (k *Keyring) For(ctx context.Context, tenant uuid.UUID) (*Sealer, error) {
	k.mu.RLock()
	s, ok := k.cache[tenant]
	k.mu.RUnlock()
	if ok {
		return s, nil
	}
	tx, err := k.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "select set_config('app.tenant_id', $1, true)", tenant.String()); err != nil {
		return nil, err
	}
	aad := []byte("dek:" + tenant.String())
	var wrapped []byte
	err = tx.QueryRow(ctx, `select wrapped_dek from tenant_key where tenant_id=$1`, tenant).Scan(&wrapped)
	if err == pgx.ErrNoRows {
		dek := make([]byte, 32)
		_, _ = rand.Read(dek)
		wrapped = k.master.Seal(dek, aad)
		if _, err := tx.Exec(ctx, `insert into tenant_key (tenant_id, wrapped_dek) values ($1, $2) on conflict do nothing`, tenant, wrapped); err != nil {
			return nil, err
		}
		if err := tx.QueryRow(ctx, `select wrapped_dek from tenant_key where tenant_id=$1`, tenant).Scan(&wrapped); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	dek, err := k.master.Open(wrapped, aad)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	s, err = New(dek)
	if err != nil {
		return nil, err
	}
	k.mu.Lock()
	k.cache[tenant] = s
	k.mu.Unlock()
	return s, nil
}

// Forget drops a cached DEK (after crypto-shredding).
func (k *Keyring) Forget(tenant uuid.UUID) {
	k.mu.Lock()
	delete(k.cache, tenant)
	k.mu.Unlock()
}

// Seal seals plain for a tenant.
func (k *Keyring) Seal(ctx context.Context, tenant uuid.UUID, plain, aad []byte) ([]byte, error) {
	s, err := k.For(ctx, tenant)
	if err != nil {
		return nil, err
	}
	return s.Seal(plain, aad), nil
}

// Open opens a value sealed for a tenant.
func (k *Keyring) Open(ctx context.Context, tenant uuid.UUID, sealed, aad []byte) ([]byte, error) {
	s, err := k.For(ctx, tenant)
	if err != nil {
		return nil, err
	}
	return s.Open(sealed, aad)
}

// Master returns the installation-level sealer (for node identity keys, not tenant data).
func (k *Keyring) Master() *Sealer { return k.master }
