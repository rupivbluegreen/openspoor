package app

import (
	"bytes"
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/secrets"
)

// RotationReport says what `openspoor rotate-keys` did (or, with a dry run, would do).
type RotationReport struct {
	secrets.RotateResult
	WebhookSecretsSealed int
	DryRun               bool
}

var errDryRun = errors.New("dry run")

// RotateKeys re-seals the installation's master-key-sealed material under newMaster. The running
// App must have been built with the current master key. Clear-text webhook secrets left from before
// sealing existed are sealed first (not in a dry run). Everything under the master key changes in
// one transaction. Afterwards every process must be restarted with the new key.
func (a *App) RotateKeys(ctx context.Context, newMaster []byte, dryRun bool) (RotationReport, error) {
	rep := RotationReport{DryRun: dryRun}
	if len(newMaster) == 0 {
		return rep, errors.New("the new master key is empty")
	}
	if bytes.Equal(newMaster, a.Deps.Cfg.MasterKey) {
		return rep, errors.New("the new master key equals the current one")
	}
	newS, err := secrets.New(newMaster)
	if err != nil {
		return rep, err
	}
	if !dryRun {
		for _, m := range a.Modules {
			if s, ok := m.(interface {
				SealLegacySecrets(context.Context) (int, error)
			}); ok {
				if n, err := s.SealLegacySecrets(ctx); err != nil {
					return rep, err
				} else {
					rep.WebhookSecretsSealed += n
				}
			}
		}
	}
	err = a.Deps.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		var err error
		rep.RotateResult, err = secrets.Rotate(ctx, tx, a.Deps.Keys.Master(), newS)
		if err == nil && dryRun {
			return errDryRun
		}
		return err
	})
	if errors.Is(err, errDryRun) {
		err = nil
	}
	return rep, err
}
