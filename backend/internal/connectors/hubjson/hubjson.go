// Package hubjson is the shared engine of connectors whose national hub interface
// specification is distributed only to onboarded parties (UAE Tatmeen, Egypt EDA). It
// speaks one minimal, documented JSON message shape to the Open Spoor mock hubs:
//
//	POST {baseUrl}/reports            body: Report        -> 202 {"reportId": "..."}
//	GET  {baseUrl}/reports/{reportId}                     -> 200 Result
//
// Every element of that shape (paths, field names, report types, authentication, the
// participant header) is an Open Spoor placeholder, marked TODO(spec), and must be mapped
// to the hub's real interface during onboarding. Field semantics (GTIN, serial, batch,
// expiry, GLNs, SSCC) follow the public GS1 General Specifications and EPCIS/CBV 2.0.
package hubjson

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/openspoor/openspoor/internal/core/egress"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	connector "github.com/openspoor/connectors-sdk"
)

// ReportTypes maps CBV business steps to the placeholder report types.
// TODO(spec): the hubs' real message/notification types are in their onboarding specs.
var ReportTypes = map[string]string{
	"commissioning":   "COMMISSION",
	"packing":         "AGGREGATION",
	"unpacking":       "DISAGGREGATION",
	"shipping":        "DISPATCH",
	"receiving":       "RECEIPT",
	"decommissioning": "DECOMMISSION",
	"destroying":      "DESTRUCTION",
	"dispensing":      "DISPENSE",
}

// Triggers returns the business steps in ReportTypes, sorted.
func Triggers() []string {
	out := make([]string, 0, len(ReportTypes))
	for k := range ReportTypes {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Profile is what differs between hubs.
type Profile struct {
	Info connector.Info
	// Hub is the short hub name used in alert titles ("Tatmeen", "EDA").
	Hub string
	// Fixes maps a hub rejection code to a suggested fix.
	Fixes map[string]string
	// DefaultFix is used for unknown codes.
	DefaultFix string
}

// Item is one serialized unit in a report.
type Item struct {
	GTIN   string `json:"gtin"`
	Serial string `json:"serial"`
	Batch  string `json:"batch,omitempty"`
	Expiry string `json:"expiry,omitempty"` // YYYY-MM-DD
}

// Report is the placeholder message body. TODO(spec): field names.
type Report struct {
	ReportType  string    `json:"reportType"`
	EventID     string    `json:"eventId"`
	EventTime   time.Time `json:"eventTime"`
	BizStep     string    `json:"bizStep"`
	SenderGLN   string    `json:"senderGln,omitempty"`
	ReceiverGLN string    `json:"receiverGln,omitempty"`
	LocationGLN string    `json:"locationGln,omitempty"`
	// Parent and Containers carry aggregation (parent/child) data.
	// TODO(spec): whether and how the hub expects aggregation reporting.
	Parent     string   `json:"parent,omitempty"`
	Containers []string `json:"containers,omitempty"`
	Items      []Item   `json:"items"`
}

// FailedItem is a unit the hub refused.
type FailedItem struct {
	GTIN   string `json:"gtin"`
	Serial string `json:"serial"`
}

// Result is the placeholder status response. TODO(spec): field names and status values.
type Result struct {
	Status       string       `json:"status"` // PENDING | ACCEPTED | REJECTED
	ErrorCode    string       `json:"errorCode,omitempty"`
	ErrorMessage string       `json:"errorMessage,omitempty"`
	FailedItems  []FailedItem `json:"failedItems,omitempty"`
}

// Connector implements connector.Connector for one Profile.
type Connector struct {
	Client  *http.Client
	Profile Profile
}

// New creates a connector for p.
func New(p Profile) *Connector {
	return &Connector{Client: egress.Client(15 * time.Second), Profile: p}
}

// Info implements connector.Connector.
func (c *Connector) Info() connector.Info { return c.Profile.Info }

// Map implements connector.Connector: one report per triggered event.
func (c *Connector) Map(_ context.Context, e connector.Event) ([]connector.Payload, error) {
	t := ReportTypes[e.BizStep]
	if t == "" || (len(e.Packs) == 0 && len(e.Containers) == 0) {
		return nil, nil
	}
	r := Report{ReportType: t, EventID: e.EventID, EventTime: e.EventTime.UTC(), BizStep: e.BizStep, SenderGLN: e.Source,
		ReceiverGLN: e.Destination, LocationGLN: e.Location, Parent: e.Parent, Items: []Item{}}
	if r.SenderGLN == "" {
		r.SenderGLN = e.Location
	}
	for _, p := range e.Packs {
		r.Items = append(r.Items, Item{GTIN: p.GTIN, Serial: p.Serial, Batch: p.Lot, Expiry: p.Expiry})
	}
	sort.Slice(r.Items, func(i, j int) bool {
		if r.Items[i].GTIN != r.Items[j].GTIN {
			return r.Items[i].GTIN < r.Items[j].GTIN
		}
		return r.Items[i].Serial < r.Items[j].Serial
	})
	r.Containers = append([]string(nil), e.Containers...)
	sort.Strings(r.Containers)
	b, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256([]byte(c.Profile.Info.ID + "|" + e.EventID + "|" + t))
	return []connector.Payload{{Kind: strings.ToLower(t), Key: hex.EncodeToString(h[:12]), Body: b, Packs: e.Packs}}, nil
}

func (c *Connector) request(ctx context.Context, cfg connector.Config, method, path, idemKey string, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(cfg["baseUrl"], "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	// TODO(spec): authentication scheme (the hubs' onboarding specs define it); basic
	// credentials are a placeholder understood by the mock hubs.
	if cfg["clientId"] != "" {
		req.SetBasicAuth(cfg["clientId"], cfg["clientSecret"])
	}
	// TODO(spec): how the reporting party identifies itself (participant ID header or field).
	if cfg["participantId"] != "" {
		req.Header.Set("X-Participant-Id", cfg["participantId"])
	}
	res, err := c.Client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return b, res.StatusCode, nil
}

// Submit implements connector.Connector.
func (c *Connector) Submit(ctx context.Context, cfg connector.Config, p connector.Payload) (connector.Submission, error) {
	b, code, err := c.request(ctx, cfg, http.MethodPost, "/reports", p.Key, p.Body)
	if err != nil {
		return connector.Submission{}, err
	}
	if code >= 300 {
		return connector.Submission{}, fmt.Errorf("%s returned %d: %s", c.Profile.Hub, code, b)
	}
	var ack struct {
		ReportID string `json:"reportId"`
	}
	if json.Unmarshal(b, &ack) != nil || ack.ReportID == "" {
		return connector.Submission{}, fmt.Errorf("unexpected %s response: %s", c.Profile.Hub, b)
	}
	return connector.Submission{Ref: ack.ReportID}, nil
}

// PollStatus implements connector.Connector.
func (c *Connector) PollStatus(ctx context.Context, cfg connector.Config, s connector.Submission) (connector.Status, error) {
	b, code, err := c.request(ctx, cfg, http.MethodGet, "/reports/"+s.Ref, "", nil)
	if err != nil {
		return connector.Status{}, err
	}
	var r Result
	if code >= 300 || json.Unmarshal(b, &r) != nil {
		return connector.Status{}, fmt.Errorf("unexpected %s response (%d): %s", c.Profile.Hub, code, b)
	}
	st := connector.Status{Raw: b}
	switch r.Status {
	case "ACCEPTED":
		st.State = connector.Accepted
	case "REJECTED":
		st.State = connector.Rejected
		rej := &connector.Rejection{Code: r.ErrorCode, Message: r.ErrorMessage}
		for _, f := range r.FailedItems {
			rej.Packs = append(rej.Packs, connector.Pack{GTIN: f.GTIN, Serial: f.Serial})
		}
		st.Rejection = rej
	default:
		st.State = connector.Pending
	}
	return st, nil
}

// HandleRejection implements connector.Connector.
func (c *Connector) HandleRejection(_ context.Context, r connector.Rejection) connector.Alert {
	a := connector.Alert{Severity: "error", Title: c.Profile.Hub + " rejected a " + strings.ToUpper(r.Payload.Kind) + " report",
		Reason: r.Code + ": " + r.Message, Packs: r.Packs, Retryable: true, SuggestedFix: c.Profile.Fixes[r.Code]}
	if a.SuggestedFix == "" {
		a.SuggestedFix = c.Profile.DefaultFix
	}
	return a
}
