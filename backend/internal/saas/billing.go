package saas

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/openspoor/openspoor/internal/core/bus"
	"github.com/openspoor/openspoor/internal/core/config"
)

type busEvent = bus.Event

// Billing receives metered usage. Self-hosted installs use the "none" adapter.
type Billing interface {
	Name() string
	ReportUsage(ctx context.Context, customer, metric, period string, delta int64) error
}

// NewBilling picks the adapter from OPENSPOOR_BILLING.
func NewBilling(cfg config.Config) Billing {
	switch cfg.BillingDriver {
	case "lago":
		return &Lago{URL: strings.TrimSuffix(cfg.LagoURL, "/"), APIKey: cfg.LagoAPIKey, Client: &http.Client{Timeout: 10 * time.Second}}
	case "stripe":
		return stripeStub{}
	}
	return noBilling{}
}

type noBilling struct{}

func (noBilling) Name() string                                                     { return "none" }
func (noBilling) ReportUsage(context.Context, string, string, string, int64) error { return nil }

// Lago sends usage events to Lago (open-source billing) as metered events.
// The tenant slug is the Lago external subscription id; the metric is the billable metric code.
type Lago struct {
	URL    string
	APIKey string
	Client *http.Client
}

// Name implements Billing.
func (l *Lago) Name() string { return "lago" }

// ReportUsage implements Billing.
func (l *Lago) ReportUsage(ctx context.Context, customer, metric, period string, delta int64) error {
	body, _ := json.Marshal(map[string]any{"event": map[string]any{
		"transaction_id":           fmt.Sprintf("%s-%s-%s-%d", customer, metric, period, time.Now().UnixNano()),
		"external_subscription_id": customer,
		"code":                     metric,
		"timestamp":                time.Now().Unix(),
		"properties":               map[string]any{"count": delta, "period": period},
	}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.URL+"/api/v1/events", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+l.APIKey)
	res, err := l.Client.Do(req)
	if err != nil {
		return err
	}
	_ = res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("lago returned %s", res.Status)
	}
	return nil
}

// stripeStub is reserved: Stripe metered billing needs per-customer subscription item ids.
// TODO: implement with Stripe Billing Meters once a SaaS operator needs it.
type stripeStub struct{}

func (stripeStub) Name() string { return "stripe" }
func (stripeStub) ReportUsage(context.Context, string, string, string, int64) error {
	return fmt.Errorf("stripe billing adapter is not implemented; use lago")
}
