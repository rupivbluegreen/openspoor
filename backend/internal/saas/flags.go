package saas

import (
	"context"
	"os"

	"github.com/Unleash/unleash-client-go/v4"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	unleashof "github.com/open-feature/go-sdk-contrib/providers/unleash/pkg"
	of "github.com/open-feature/go-sdk/openfeature"

	"github.com/openspoor/openspoor/internal/core/config"
)

// PlanProvider is an OpenFeature provider that resolves flags from the tenant's plan.
// Self-hosted (single/hub) installs get every feature.
type PlanProvider struct{ m *Module }

// Metadata implements of.FeatureProvider.
func (p *PlanProvider) Metadata() of.Metadata { return of.Metadata{Name: "openspoor-plan"} }

// Hooks implements of.FeatureProvider.
func (p *PlanProvider) Hooks() []of.Hook { return nil }

// BooleanEvaluation implements of.FeatureProvider.
func (p *PlanProvider) BooleanEvaluation(ctx context.Context, flag string, def bool, fc of.FlattenedContext) of.BoolResolutionDetail {
	if p.m.Cfg.Mode != config.ModeSaaS {
		return of.BoolResolutionDetail{Value: true, ProviderResolutionDetail: of.ProviderResolutionDetail{Reason: of.StaticReason}}
	}
	tid, err := uuid.Parse(fmtKey(fc))
	if err != nil {
		return of.BoolResolutionDetail{Value: def, ProviderResolutionDetail: of.ProviderResolutionDetail{Reason: of.DefaultReason}}
	}
	var plan string
	_ = p.m.DB.Tx(ctx, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select plan from tenant where id=app_tenant()`).Scan(&plan)
	})
	pl, ok := Plans[plan]
	if !ok {
		return of.BoolResolutionDetail{Value: def, ProviderResolutionDetail: of.ProviderResolutionDetail{Reason: of.DefaultReason}}
	}
	return of.BoolResolutionDetail{Value: pl.Features[flag], ProviderResolutionDetail: of.ProviderResolutionDetail{Reason: of.TargetingMatchReason, Variant: plan}}
}

func fmtKey(fc of.FlattenedContext) string {
	if v, ok := fc[of.TargetingKey].(string); ok {
		return v
	}
	return ""
}

// StringEvaluation implements of.FeatureProvider.
func (p *PlanProvider) StringEvaluation(_ context.Context, _ string, def string, _ of.FlattenedContext) of.StringResolutionDetail {
	return of.StringResolutionDetail{Value: def, ProviderResolutionDetail: of.ProviderResolutionDetail{Reason: of.DefaultReason}}
}

// FloatEvaluation implements of.FeatureProvider.
func (p *PlanProvider) FloatEvaluation(_ context.Context, _ string, def float64, _ of.FlattenedContext) of.FloatResolutionDetail {
	return of.FloatResolutionDetail{Value: def, ProviderResolutionDetail: of.ProviderResolutionDetail{Reason: of.DefaultReason}}
}

// IntEvaluation implements of.FeatureProvider.
func (p *PlanProvider) IntEvaluation(_ context.Context, _ string, def int64, _ of.FlattenedContext) of.IntResolutionDetail {
	return of.IntResolutionDetail{Value: def, ProviderResolutionDetail: of.ProviderResolutionDetail{Reason: of.DefaultReason}}
}

// ObjectEvaluation implements of.FeatureProvider.
func (p *PlanProvider) ObjectEvaluation(_ context.Context, _ string, def any, _ of.FlattenedContext) of.InterfaceResolutionDetail {
	return of.InterfaceResolutionDetail{Value: def, ProviderResolutionDetail: of.ProviderResolutionDetail{Reason: of.DefaultReason}}
}

// unleashProvider returns the OpenFeature Unleash provider when OPENSPOOR_UNLEASH_URL is set.
func unleashProvider(_ config.Config) (of.FeatureProvider, error) {
	url := os.Getenv("OPENSPOOR_UNLEASH_URL")
	if url == "" {
		return nil, nil
	}
	return unleashof.NewProvider(unleashof.ProviderConfig{Options: []unleash.ConfigOption{
		unleash.WithAppName("openspoor"), unleash.WithUrl(url),
		unleash.WithCustomHeaders(map[string][]string{"Authorization": {os.Getenv("OPENSPOOR_UNLEASH_TOKEN")}}),
	}})
}
