package app

import "context"

// Seed loads demo data (implemented in seed_data.go).
func (a *App) Seed(ctx context.Context, packs int, password string) error {
	return seedDemo(ctx, a, packs, password)
}
