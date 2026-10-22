// Package eufmd is the reference EU FMD connector: it uploads pack data (product code,
// batch, expiry, serial numbers) to the EU Hub / national system (EMVS) when packs are
// commissioned, and reports status changes (decommissioning: dispensed, destroyed,
// sample, export, stolen) per Commission Delegated Regulation (EU) 2016/161.
//
// TODO(spec): the EU Hub interface specification is distributed by EMVO to onboarded
// partners and is not public. Endpoint paths, field names and error codes below are
// those of the Open Spoor mock hub (internal/connectors/mockhub) and must be mapped
// to the real interface during onboarding.
package eufmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	connector "github.com/openspoor/connectors-sdk"
)

// Connector implements connector.Connector.
type Connector struct{ Client *http.Client }

// New creates the connector.
func New() *Connector { return &Connector{Client: &http.Client{Timeout: 15 * time.Second}} }

// Info implements connector.Connector.
func (c *Connector) Info() connector.Info {
	return connector.Info{
		ID: "eu-fmd-emvs", Name: "EU FMD — EMVS pack data upload", Country: "EU", Market: "EU",
		Description: "Uploads unique identifiers on commissioning and reports decommissioning to the EU Hub/EMVS (Delegated Regulation 2016/161).",
		Triggers:    []string{"commissioning", "dispensing", "destroying", "decommissioning", "sampling"},
		Spec:        "Commission Delegated Regulation (EU) 2016/161, Articles 4, 33 and 35",
		Config: []connector.ConfigField{
			{Key: "baseUrl", Label: "EU Hub base URL", Required: true, Default: "local-mock", Help: "TODO(spec): real EU Hub URL from EMVO onboarding"},
			{Key: "clientId", Label: "OBP client ID", Required: false},
			{Key: "apiKey", Label: "API key", Secret: true},
			{Key: "market", Label: "Target market code (e.g. NL, DE)", Default: "EU"},
		},
	}
}

func key(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(h[:12])
}

type upload struct {
	ProductCode string   `json:"productCode"` // GTIN
	ProductName string   `json:"productName"`
	Batch       string   `json:"batch"`
	Expiry      string   `json:"expiry"`
	Serials     []string `json:"serials"`
}

type statusUpdate struct {
	ProductCode string   `json:"productCode"`
	Batch       string   `json:"batch"`
	Serials     []string `json:"serials"`
	NewState    string   `json:"newState"`
}

var decommission = map[string]string{"dispensing": "SUPPLIED", "destroying": "DESTROYED", "decommissioning": "DECOMMISSIONED", "sampling": "SAMPLE"}

// Map implements connector.Connector: one payload per (GTIN, batch) group.
func (c *Connector) Map(_ context.Context, e connector.Event) ([]connector.Payload, error) {
	type grp struct {
		gtin, lot, exp string
	}
	groups := map[grp][]connector.Pack{}
	for _, p := range e.Packs {
		g := grp{p.GTIN, p.Lot, p.Expiry}
		groups[g] = append(groups[g], p)
	}
	keys := make([]grp, 0, len(groups))
	for g := range groups {
		keys = append(keys, g)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].gtin+keys[i].lot < keys[j].gtin+keys[j].lot })
	var out []connector.Payload
	for _, g := range keys {
		packs := groups[g]
		serials := make([]string, len(packs))
		for i, p := range packs {
			serials[i] = p.Serial
		}
		sort.Strings(serials)
		var body any
		kind := ""
		switch {
		case e.BizStep == "commissioning" && e.Action == "ADD":
			kind = "pack-data-upload"
			body = upload{ProductCode: g.gtin, ProductName: e.Products[g.gtin].Name, Batch: g.lot, Expiry: g.exp, Serials: serials}
		case decommission[e.BizStep] != "":
			kind = "pack-status-update"
			body = statusUpdate{ProductCode: g.gtin, Batch: g.lot, Serials: serials, NewState: decommission[e.BizStep]}
		default:
			continue
		}
		b, _ := json.Marshal(body)
		out = append(out, connector.Payload{Kind: kind, Key: key(e.EventID, kind, g.gtin, g.lot), Body: b, Packs: packs})
	}
	return out, nil
}

func (c *Connector) base(cfg connector.Config) string { return strings.TrimSuffix(cfg["baseUrl"], "/") }

// Submit implements connector.Connector.
func (c *Connector) Submit(ctx context.Context, cfg connector.Config, p connector.Payload) (connector.Submission, error) {
	path := "/v1/packdata"
	if p.Kind == "pack-status-update" {
		path = "/v1/packstatus"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base(cfg)+path, bytes.NewReader(p.Body))
	if err != nil {
		return connector.Submission{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", p.Key)
	if k := cfg["apiKey"]; k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	res, err := c.Client.Do(req)
	if err != nil {
		return connector.Submission{}, err
	}
	defer res.Body.Close()
	var ack struct {
		TransactionID string `json:"transactionId"`
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return connector.Submission{}, fmt.Errorf("hub returned %d: %s", res.StatusCode, b)
	}
	if err := json.Unmarshal(b, &ack); err != nil || ack.TransactionID == "" {
		return connector.Submission{}, fmt.Errorf("unexpected hub response: %s", b)
	}
	return connector.Submission{Ref: ack.TransactionID}, nil
}

// PollStatus implements connector.Connector.
func (c *Connector) PollStatus(ctx context.Context, cfg connector.Config, s connector.Submission) (connector.Status, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.base(cfg)+"/v1/transactions/"+s.Ref, nil)
	if k := cfg["apiKey"]; k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	res, err := c.Client.Do(req)
	if err != nil {
		return connector.Status{}, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var tx struct {
		Status string `json:"status"`
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Serial  string `json:"serial"`
		} `json:"errors"`
		ProductCode string `json:"productCode"`
		Batch       string `json:"batch"`
	}
	if err := json.Unmarshal(b, &tx); err != nil {
		return connector.Status{}, fmt.Errorf("unexpected hub response: %s", b)
	}
	st := connector.Status{Raw: b}
	switch tx.Status {
	case "PROCESSED":
		st.State = connector.Accepted
	case "REJECTED":
		st.State = connector.Rejected
		r := &connector.Rejection{}
		if len(tx.Errors) > 0 {
			r.Code, r.Message = tx.Errors[0].Code, tx.Errors[0].Message
		}
		for _, e := range tx.Errors {
			if e.Serial != "" {
				r.Packs = append(r.Packs, connector.Pack{GTIN: tx.ProductCode, Serial: e.Serial, Lot: tx.Batch})
			}
		}
		st.Rejection = r
	default:
		st.State = connector.Pending
	}
	return st, nil
}

// HandleRejection implements connector.Connector.
func (c *Connector) HandleRejection(_ context.Context, r connector.Rejection) connector.Alert {
	a := connector.Alert{Severity: "error", Title: "EU Hub rejected " + r.Payload.Kind, Reason: r.Code + ": " + r.Message, Packs: r.Packs, Retryable: true}
	switch r.Code {
	case "MOCK-PRODUCT-UNKNOWN":
		a.SuggestedFix = "Upload the product master data (GTIN, name, markets) to the EU Hub, then resubmit."
	case "MOCK-SERIAL-DUPLICATE":
		a.SuggestedFix = "These serials already exist in the hub. Check for a double commissioning; if the earlier upload is correct, close this alert."
		a.Retryable = false
	case "MOCK-BATCH-MISSING":
		a.SuggestedFix = "Add the batch number (ILMD lotNumber) to the commissioning event and recapture, then resubmit."
	case "MOCK-EXPIRY-INVALID":
		a.SuggestedFix = "Expiry must be a date (YYYY-MM-DD) in the future; correct ILMD itemExpirationDate and resubmit."
	case "MOCK-SERIAL-UNKNOWN":
		a.SuggestedFix = "Pack data was never uploaded for these serials; resubmit the commissioning upload first."
	default:
		a.SuggestedFix = "Review the hub message; correct the source data and resubmit."
	}
	return a
}
