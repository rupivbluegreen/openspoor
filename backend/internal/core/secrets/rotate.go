package secrets

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// RotateResult counts what Rotate re-sealed.
type RotateResult struct {
	TenantKeys int // per-tenant data keys re-wrapped
	NodeKeys   int // the federation node signing key (sealed directly under the master key)
}

// Rotate re-seals everything that is sealed directly under the master key from oldMaster to newMaster:
// the per-tenant data keys (tenant_key) and the federation node key (federation_node). Values sealed
// with a tenant data key (webhook secrets, connector and pool credentials, TOTP secrets) are not touched:
// the data keys themselves do not change, which is the point of the key hierarchy.
//
// tx must be an operator transaction (it reads all tenants). Rotate locks both tables against concurrent
// writers, opens every value with the old key before replacing it, and opens the replacement with the new
// key before returning, so a wrong old key or a faulty write fails the whole transaction and nothing changes.
// Callers should stop the API and worker first and restart them with the new key afterwards: a process
// still using the old key cannot read what was re-sealed.
func Rotate(ctx context.Context, tx pgx.Tx, oldMaster, newMaster *Sealer) (RotateResult, error) {
	var res RotateResult
	if _, err := tx.Exec(ctx, `lock table tenant_key, federation_node in exclusive mode`); err != nil {
		return res, err
	}
	type dek struct {
		tenant  uuid.UUID
		wrapped []byte
	}
	var keys []dek
	rows, err := tx.Query(ctx, `select tenant_id, wrapped_dek from tenant_key order by tenant_id`)
	if err != nil {
		return res, err
	}
	for rows.Next() {
		var k dek
		if err := rows.Scan(&k.tenant, &k.wrapped); err != nil {
			rows.Close()
			return res, err
		}
		keys = append(keys, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	for _, k := range keys {
		aad := []byte("dek:" + k.tenant.String())
		plain, err := oldMaster.Open(k.wrapped, aad)
		if err != nil {
			return res, fmt.Errorf("tenant %s: the current master key cannot open its data key (is OPENSPOOR_MASTER_KEY the key in use?): %w", k.tenant, err)
		}
		sealed := newMaster.Seal(plain, aad)
		if err := checkRoundTrip(newMaster, sealed, aad, plain); err != nil {
			return res, fmt.Errorf("tenant %s: %w", k.tenant, err)
		}
		if _, err := tx.Exec(ctx, `update tenant_key set wrapped_dek=$2 where tenant_id=$1`, k.tenant, sealed); err != nil {
			return res, err
		}
		res.TenantKeys++
	}
	var nodeKey []byte
	err = tx.QueryRow(ctx, `select private_key from federation_node where id=1`).Scan(&nodeKey)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return res, err
	default:
		aad := []byte("federation-node")
		plain, err := oldMaster.Open(nodeKey, aad)
		if err != nil {
			return res, fmt.Errorf("federation node key: the current master key cannot open it: %w", err)
		}
		sealed := newMaster.Seal(plain, aad)
		if err := checkRoundTrip(newMaster, sealed, aad, plain); err != nil {
			return res, fmt.Errorf("federation node key: %w", err)
		}
		if _, err := tx.Exec(ctx, `update federation_node set private_key=$1 where id=1`, sealed); err != nil {
			return res, err
		}
		res.NodeKeys++
	}
	return res, nil
}

func checkRoundTrip(s *Sealer, sealed, aad, want []byte) error {
	got, err := s.Open(sealed, aad)
	if err != nil || !bytes.Equal(got, want) {
		return errors.New("the re-sealed value does not open with the new master key")
	}
	return nil
}
