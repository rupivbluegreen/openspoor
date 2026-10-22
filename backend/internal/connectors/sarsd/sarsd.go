// Package sarsd is the reference connector for the Saudi Food and Drug Authority's
// Drug Track and Trace System (RSD). It notifies production (commissioning), dispatch
// (shipping, with the receiver's GLN), acceptance (receiving) and deactivation.
//
// TODO(spec): RSD exposes SOAP web services documented in SFDA's integration guide.
// This connector speaks JSON to the Open Spoor mock hub; the SOAP envelope, service
// names and notification codes must be mapped from the current SFDA guide before
// production. Field semantics (GTIN, serial, batch, expiry, GLNs) follow SFDA's
// published drug barcoding specification (GS1 DataMatrix: 01, 21, 10, 17).
package sarsd

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
		ID: "sa-rsd", Name: "Saudi Arabia — SFDA RSD notifications", Country: "SA", Market: "SA",
		Description: "Sends production, dispatch, accept and deactivation notifications to the SFDA Drug Track and Trace System.",
		Triggers:    []string{"commissioning", "shipping", "receiving", "decommissioning", "destroying", "dispensing"},
		Spec:        "SFDA Drug Barcoding Specifications; RSD integration guide (TODO(spec): version)",
		Config: []connector.ConfigField{
			{Key: "baseUrl", Label: "RSD endpoint", Required: true, Default: "local-mock", Help: "TODO(spec): SFDA RSD service URL"},
			{Key: "username", Label: "RSD username"},
			{Key: "password", Label: "RSD password", Secret: true},
			{Key: "senderGln", Label: "Sender GLN (stakeholder)", Required: false},
		},
	}
}

var notif = map[string]string{"commissioning": "PRODUCTION", "shipping": "DISPATCH", "receiving": "ACCEPT", "decommissioning": "DEACTIVATION",
	"destroying": "DEACTIVATION", "dispensing": "PHARMACY_SALE"}

type notification struct {
	Type      string        `json:"notificationType"`
	FromGLN   string        `json:"fromGln,omitempty"`
	ToGLN     string        `json:"toGln,omitempty"`
	EventTime time.Time     `json:"eventTime"`
	Products  []productLine `json:"products"`
}

type productLine struct {
	GTIN    string   `json:"gtin"`
	Batch   string   `json:"batch"`
	Expiry  string   `json:"expiry"`
	Serials []string `json:"serials"`
}

// Map implements connector.Connector: one notification per event.
func (c *Connector) Map(_ context.Context, e connector.Event) ([]connector.Payload, error) {
	t := notif[e.BizStep]
	if t == "" || len(e.Packs) == 0 {
		return nil, nil
	}
	lines := map[string]*productLine{}
	for _, p := range e.Packs {
		k := p.GTIN + "|" + p.Lot
		if lines[k] == nil {
			lines[k] = &productLine{GTIN: p.GTIN, Batch: p.Lot, Expiry: p.Expiry}
		}
		lines[k].Serials = append(lines[k].Serials, p.Serial)
	}
	keys := make([]string, 0, len(lines))
	for k := range lines {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	n := notification{Type: t, FromGLN: e.Source, ToGLN: e.Destination, EventTime: e.EventTime.UTC()}
	if n.FromGLN == "" {
		n.FromGLN = e.Location
	}
	for _, k := range keys {
		sort.Strings(lines[k].Serials)
		n.Products = append(n.Products, *lines[k])
	}
	b, _ := json.Marshal(n)
	h := sha256.Sum256([]byte(e.EventID + "|" + t))
	return []connector.Payload{{Kind: strings.ToLower(t), Key: hex.EncodeToString(h[:12]), Body: b, Packs: e.Packs}}, nil
}

// Submit implements connector.Connector.
func (c *Connector) Submit(ctx context.Context, cfg connector.Config, p connector.Payload) (connector.Submission, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(cfg["baseUrl"], "/")+"/notifications", bytes.NewReader(p.Body))
	if err != nil {
		return connector.Submission{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", p.Key)
	if cfg["username"] != "" {
		req.SetBasicAuth(cfg["username"], cfg["password"])
	}
	res, err := c.Client.Do(req)
	if err != nil {
		return connector.Submission{}, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return connector.Submission{}, fmt.Errorf("RSD returned %d: %s", res.StatusCode, b)
	}
	var ack struct {
		NotificationID string `json:"notificationId"`
	}
	if json.Unmarshal(b, &ack) != nil || ack.NotificationID == "" {
		return connector.Submission{}, fmt.Errorf("unexpected RSD response: %s", b)
	}
	return connector.Submission{Ref: ack.NotificationID}, nil
}

// PollStatus implements connector.Connector.
func (c *Connector) PollStatus(ctx context.Context, cfg connector.Config, s connector.Submission) (connector.Status, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(cfg["baseUrl"], "/")+"/notifications/"+s.Ref, nil)
	if cfg["username"] != "" {
		req.SetBasicAuth(cfg["username"], cfg["password"])
	}
	res, err := c.Client.Do(req)
	if err != nil {
		return connector.Status{}, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var r struct {
		Result      string `json:"result"`
		ReasonCode  string `json:"reasonCode"`
		Description string `json:"description"`
		Serials     []struct {
			GTIN, Serial string
		} `json:"failedSerials"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return connector.Status{}, fmt.Errorf("unexpected RSD response: %s", b)
	}
	st := connector.Status{Raw: b}
	switch r.Result {
	case "SUCCESS":
		st.State = connector.Accepted
	case "FAILED":
		st.State = connector.Rejected
		rej := &connector.Rejection{Code: r.ReasonCode, Message: r.Description}
		for _, s := range r.Serials {
			rej.Packs = append(rej.Packs, connector.Pack{GTIN: s.GTIN, Serial: s.Serial})
		}
		st.Rejection = rej
	default:
		st.State = connector.Pending
	}
	return st, nil
}

// HandleRejection implements connector.Connector.
func (c *Connector) HandleRejection(_ context.Context, r connector.Rejection) connector.Alert {
	a := connector.Alert{Severity: "error", Title: "SFDA RSD rejected a " + strings.ToUpper(r.Payload.Kind) + " notification", Reason: r.Code + ": " + r.Message, Packs: r.Packs, Retryable: true}
	switch r.Code {
	case "MOCK-RECEIVER-GLN-MISSING":
		a.SuggestedFix = "Dispatch needs the receiver's GLN: add a destinationList owning_party/location to the shipping event (or set the partner GLN) and resubmit."
	case "MOCK-NOT-PRODUCED":
		a.SuggestedFix = "These serials were never notified as produced. Submit the PRODUCTION notification first, then resubmit."
	case "MOCK-GTIN-NOT-REGISTERED":
		a.SuggestedFix = "Register the GTIN with SFDA (drug registration / barcode listing), then resubmit."
	default:
		a.SuggestedFix = "Review the RSD reason, correct the source data and resubmit."
	}
	return a
}
