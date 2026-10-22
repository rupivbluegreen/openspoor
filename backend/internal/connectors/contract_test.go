package connectors_test

import (
	"net/http"
	"testing"
	"time"

	connector "github.com/openspoor/openspoor/connectors-sdk"
	"github.com/openspoor/openspoor/connectors-sdk/contracttest"
	"github.com/openspoor/openspoor/internal/connectors/eufmd"
	"github.com/openspoor/openspoor/internal/connectors/mockhub"
	"github.com/openspoor/openspoor/internal/connectors/sarsd"
)

func ev(step, action string, lot, exp string, dest string) connector.Event {
	return connector.Event{EventID: "ev-" + step + lot, Type: "ObjectEvent", Action: action, BizStep: step, EventTime: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		Destination: dest, Products: map[string]connector.Product{"09506000134352": {GTIN: "09506000134352", Name: "Test"}},
		Packs: []connector.Pack{{GTIN: "09506000134352", Serial: "S1" + lot, Lot: lot, Expiry: exp}, {GTIN: "09506000134352", Serial: "S2" + lot, Lot: lot, Expiry: exp}}}
}

func TestEUFMDContract(t *testing.T) {
	mt := mockhub.NewTransport()
	mt.RegisterEUProduct("09506000134352")
	c := eufmd.New()
	c.Client = &http.Client{Transport: mt}
	future := time.Now().AddDate(2, 0, 0).Format("2006-01-02")
	contracttest.Run(t, c, connector.Config{"baseUrl": "http://" + mockhub.EUHost}, []contracttest.Case{
		{Name: "commissioning accepted", Event: ev("commissioning", "ADD", "B1", future, ""), WantPayloads: 1, WantState: connector.Accepted},
		{Name: "dispensing accepted", Event: ev("dispensing", "OBSERVE", "B1", future, ""), WantPayloads: 1, WantState: connector.Accepted},
		{Name: "missing batch rejected", Event: ev("commissioning", "ADD", "", future, ""), WantPayloads: 1, WantState: connector.Rejected},
		{Name: "expired rejected", Event: ev("commissioning", "ADD", "B2", "2020-01-01", ""), WantPayloads: 1, WantState: connector.Rejected},
		{Name: "shipping is not reported", Event: ev("shipping", "OBSERVE", "B1", future, ""), WantPayloads: 0},
	})
}

func TestSARSDContract(t *testing.T) {
	c := sarsd.New()
	c.Client = &http.Client{Transport: mockhub.NewTransport()}
	cfg := connector.Config{"baseUrl": "http://" + mockhub.SAHost}
	contracttest.Run(t, c, cfg, []contracttest.Case{
		{Name: "production", Event: ev("commissioning", "ADD", "L1", "2030-01-01", ""), WantPayloads: 1, WantState: connector.Accepted},
		{Name: "dispatch with receiver", Event: ev("shipping", "OBSERVE", "L1", "2030-01-01", "6281000000003"), WantPayloads: 1, WantState: connector.Accepted},
		{Name: "dispatch without receiver", Event: ev("shipping", "OBSERVE", "L1", "2030-01-01", ""), WantPayloads: 1, WantState: connector.Rejected},
		{Name: "accept of unknown packs", Event: ev("receiving", "OBSERVE", "ZZ", "2030-01-01", ""), WantPayloads: 1, WantState: connector.Rejected},
	})
}
