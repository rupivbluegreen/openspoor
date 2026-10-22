// Package connector is the Open Spoor country-connector SDK.
//
// A connector turns EPCIS events into a regulator's (or national hub's) reports:
//
//	Map(event) -> payloads      pure, deterministic, no I/O
//	Submit(payload)             send one payload, return a submission reference
//	PollStatus(submission)      pending | accepted | rejected
//	HandleRejection(rejection)  parse the hub's reason into an actionable alert
//
// The host (Open Spoor) owns retries with backoff, idempotency, audit, the alert
// inbox and one-click resubmission. Connectors never invent regulatory rules: unknown
// hub fields are stubbed and marked TODO(spec).
package connector

import (
	"context"
	"encoding/json"
	"time"
)

// Info describes a connector.
type Info struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Country     string         `json:"country"`
	Market      string         `json:"market"`
	Description string         `json:"description"`
	Triggers    []string       `json:"triggers"` // CBV business steps that produce reports
	Config      []ConfigField  `json:"config"`
	Spec        string         `json:"spec"` // public specification the connector follows
	Extra       map[string]any `json:"extra,omitempty"`
}

// ConfigField describes one configuration value.
type ConfigField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Secret   bool   `json:"secret"`
	Required bool   `json:"required"`
	Default  string `json:"default,omitempty"`
	Help     string `json:"help,omitempty"`
}

// Config is an instance's configuration (secrets already decrypted by the host).
type Config map[string]string

// Pack identifies one serialized unit.
type Pack struct {
	GTIN   string `json:"gtin"`
	Serial string `json:"serial"`
	Lot    string `json:"lot,omitempty"`
	Expiry string `json:"expiry,omitempty"` // YYYY-MM-DD
}

// Product is the master data a connector may need.
type Product struct {
	GTIN        string            `json:"gtin"`
	Name        string            `json:"name"`
	NDC         string            `json:"ndc,omitempty"`
	MarketCodes map[string]string `json:"marketCodes,omitempty"`
	DosageForm  string            `json:"dosageForm,omitempty"`
	Strength    string            `json:"strength,omitempty"`
	PackSize    string            `json:"packSize,omitempty"`
}

// Event is the connector's view of one EPCIS event, enriched with master data.
type Event struct {
	EventID     string             `json:"eventId"`
	Type        string             `json:"type"`
	Action      string             `json:"action"`
	BizStep     string             `json:"bizStep"`
	Disposition string             `json:"disposition"`
	EventTime   time.Time          `json:"eventTime"`
	Packs       []Pack             `json:"packs"`
	Containers  []string           `json:"containers,omitempty"`
	Location    string             `json:"location,omitempty"` // GLN
	Source      string             `json:"source,omitempty"`   // GLN of owning party
	Destination string             `json:"destination,omitempty"`
	Products    map[string]Product `json:"products"` // by GTIN
}

// Payload is one report to submit.
type Payload struct {
	Kind  string          `json:"kind"`
	Key   string          `json:"key"` // idempotency key, stable for the same input
	Body  json.RawMessage `json:"body"`
	Packs []Pack          `json:"packs"`
}

// Submission is a hub's acknowledgement.
type Submission struct {
	Ref string `json:"ref"`
}

// State of a submission.
type State string

// States.
const (
	Pending  State = "pending"
	Accepted State = "accepted"
	Rejected State = "rejected"
)

// Status is the result of PollStatus.
type Status struct {
	State     State           `json:"state"`
	Rejection *Rejection      `json:"rejection,omitempty"`
	Raw       json.RawMessage `json:"raw,omitempty"`
}

// Rejection is a hub's refusal.
type Rejection struct {
	Code    string  `json:"code"`
	Message string  `json:"message"`
	Packs   []Pack  `json:"packs,omitempty"`
	Payload Payload `json:"payload"`
}

// Alert is what a person sees in the regulatory alert inbox.
type Alert struct {
	Severity     string `json:"severity"` // warning | error
	Title        string `json:"title"`
	Reason       string `json:"reason"`
	SuggestedFix string `json:"suggestedFix"`
	Packs        []Pack `json:"packs,omitempty"`
	Retryable    bool   `json:"retryable"`
}

// Connector is implemented by every country connector.
type Connector interface {
	Info() Info
	Map(ctx context.Context, e Event) ([]Payload, error)
	Submit(ctx context.Context, cfg Config, p Payload) (Submission, error)
	PollStatus(ctx context.Context, cfg Config, s Submission) (Status, error)
	HandleRejection(ctx context.Context, r Rejection) Alert
}
