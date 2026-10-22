// Package epcis is an Apache-2.0 implementation of GS1 EPCIS 2.0 documents:
// JSON-LD and XML reading (with EPCIS 1.2 XML read compatibility), JSON-LD and XML
// writing, CBV 2.0 validation and identifier normalization. It depends only on the
// public GS1 standards and has no Open Spoor internals, so it is reusable on its own.
package epcis

import (
	"encoding/json"
	"time"
)

// Event types.
const (
	ObjectEvent         = "ObjectEvent"
	AggregationEvent    = "AggregationEvent"
	TransactionEvent    = "TransactionEvent"
	TransformationEvent = "TransformationEvent"
	AssociationEvent    = "AssociationEvent"
)

// Actions.
const (
	ActionAdd     = "ADD"
	ActionObserve = "OBSERVE"
	ActionDelete  = "DELETE"
)

// DefaultContext is the EPCIS 2.0 JSON-LD context.
const DefaultContext = "https://ref.gs1.org/standards/epcis/2.0.0/epcis-context.jsonld"

// Document is an EPCISDocument (or EPCISQueryDocument when QueryResults is set).
type Document struct {
	Context       any             `json:"@context,omitempty"`
	Type          string          `json:"type"`
	SchemaVersion string          `json:"schemaVersion"`
	CreationDate  time.Time       `json:"creationDate"`
	Header        json.RawMessage `json:"epcisHeader,omitempty"`
	Body          Body            `json:"epcisBody"`
}

// Body holds events (capture documents) or query results.
type Body struct {
	EventList    []Event       `json:"eventList,omitempty"`
	QueryResults *QueryResults `json:"queryResults,omitempty"`
}

// QueryResults is the query document body.
type QueryResults struct {
	QueryName      string `json:"queryName"`
	SubscriptionID string `json:"subscriptionID,omitempty"`
	ResultsBody    struct {
		EventList []Event `json:"eventList"`
	} `json:"resultsBody"`
}

// QuantityElement is a class-level quantity.
type QuantityElement struct {
	EPCClass string   `json:"epcClass"`
	Quantity *float64 `json:"quantity,omitempty"`
	UOM      string   `json:"uom,omitempty"`
}

// BizTransaction references a business transaction.
type BizTransaction struct {
	Type           string `json:"type,omitempty"`
	BizTransaction string `json:"bizTransaction"`
}

// Source is a source list entry.
type Source struct {
	Type   string `json:"type"`
	Source string `json:"source"`
}

// Destination is a destination list entry.
type Destination struct {
	Type        string `json:"type"`
	Destination string `json:"destination"`
}

// Location is a read point or business location.
type Location struct {
	ID string `json:"id"`
}

// ErrorDeclaration marks an earlier event as erroneous.
type ErrorDeclaration struct {
	DeclarationTime    time.Time `json:"declarationTime"`
	Reason             string    `json:"reason,omitempty"`
	CorrectiveEventIDs []string  `json:"correctiveEventIDs,omitempty"`
}

// PersistentDisposition sets/unsets persistent dispositions.
type PersistentDisposition struct {
	Set   []string `json:"set,omitempty"`
	Unset []string `json:"unset,omitempty"`
}

// Event is any EPCIS event. Fields not used by a type stay empty.
// Unknown (extension) properties are kept in Extensions verbatim.
type Event struct {
	Type                  string                 `json:"type"`
	EventID               string                 `json:"eventID,omitempty"`
	EventTime             time.Time              `json:"eventTime"`
	EventTimeZoneOffset   string                 `json:"eventTimeZoneOffset"`
	RecordTime            *time.Time             `json:"recordTime,omitempty"`
	ErrorDeclaration      *ErrorDeclaration      `json:"errorDeclaration,omitempty"`
	ParentID              string                 `json:"parentID,omitempty"`
	EPCList               []string               `json:"epcList,omitempty"`
	ChildEPCs             []string               `json:"childEPCs,omitempty"`
	InputEPCList          []string               `json:"inputEPCList,omitempty"`
	OutputEPCList         []string               `json:"outputEPCList,omitempty"`
	QuantityList          []QuantityElement      `json:"quantityList,omitempty"`
	ChildQuantityList     []QuantityElement      `json:"childQuantityList,omitempty"`
	InputQuantityList     []QuantityElement      `json:"inputQuantityList,omitempty"`
	OutputQuantityList    []QuantityElement      `json:"outputQuantityList,omitempty"`
	TransformationID      string                 `json:"transformationID,omitempty"`
	Action                string                 `json:"action,omitempty"`
	BizStep               string                 `json:"bizStep,omitempty"`
	Disposition           string                 `json:"disposition,omitempty"`
	PersistentDisposition *PersistentDisposition `json:"persistentDisposition,omitempty"`
	ReadPoint             *Location              `json:"readPoint,omitempty"`
	BizLocation           *Location              `json:"bizLocation,omitempty"`
	BizTransactionList    []BizTransaction       `json:"bizTransactionList,omitempty"`
	SourceList            []Source               `json:"sourceList,omitempty"`
	DestinationList       []Destination          `json:"destinationList,omitempty"`
	SensorElementList     json.RawMessage        `json:"sensorElementList,omitempty"`
	CertificationInfo     any                    `json:"certificationInfo,omitempty"`
	ILMD                  map[string]any         `json:"ilmd,omitempty"`
	Extensions            map[string]any         `json:"-"`
}

// AllEPCs returns every instance-level identifier the event touches (parent included).
func (e *Event) AllEPCs() []string {
	n := len(e.EPCList) + len(e.ChildEPCs) + len(e.InputEPCList) + len(e.OutputEPCList) + 1
	out := make([]string, 0, n)
	if e.ParentID != "" {
		out = append(out, e.ParentID)
	}
	out = append(out, e.EPCList...)
	out = append(out, e.ChildEPCs...)
	out = append(out, e.InputEPCList...)
	out = append(out, e.OutputEPCList...)
	for _, q := range [][]QuantityElement{e.QuantityList, e.ChildQuantityList, e.InputQuantityList, e.OutputQuantityList} {
		for _, x := range q {
			out = append(out, x.EPCClass)
		}
	}
	return out
}

// knownFields are JSON properties mapped to struct fields.
var knownFields = map[string]bool{
	"type": true, "eventID": true, "eventTime": true, "eventTimeZoneOffset": true, "recordTime": true, "errorDeclaration": true,
	"parentID": true, "epcList": true, "childEPCs": true, "inputEPCList": true, "outputEPCList": true, "quantityList": true,
	"childQuantityList": true, "inputQuantityList": true, "outputQuantityList": true, "transformationID": true, "action": true,
	"bizStep": true, "disposition": true, "persistentDisposition": true, "readPoint": true, "bizLocation": true,
	"bizTransactionList": true, "sourceList": true, "destinationList": true, "sensorElementList": true, "certificationInfo": true,
	"ilmd": true, "@context": true,
}

type eventAlias Event

// UnmarshalJSON keeps extension properties.
func (e *Event) UnmarshalJSON(b []byte) error {
	var a eventAlias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*e = Event(a)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	for k, v := range raw {
		if knownFields[k] {
			continue
		}
		if e.Extensions == nil {
			e.Extensions = map[string]any{}
		}
		var x any
		_ = json.Unmarshal(v, &x)
		e.Extensions[k] = x
	}
	return nil
}

// MarshalJSON writes extension properties inline.
func (e Event) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(eventAlias(e))
	if err != nil || len(e.Extensions) == 0 {
		return b, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range e.Extensions {
		if _, exists := m[k]; !exists {
			m[k] = v
		}
	}
	return json.Marshal(m)
}
