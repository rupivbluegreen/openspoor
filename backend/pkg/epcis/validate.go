package epcis

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/openspoor/openspoor/pkg/gs1"
)

// FieldError is one validation problem in one event.
type FieldError struct {
	Index   int    `json:"index"`
	EventID string `json:"eventID,omitempty"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (f FieldError) Error() string {
	return fmt.Sprintf("event %d: %s: %s", f.Index, f.Field, f.Message)
}

var tzRe = regexp.MustCompile(`^[+-](0\d|1[0-4]):[0-5]\d$`)

// Normalize rewrites CBV values to bare terms and fills the time zone offset if missing.
func Normalize(e *Event) {
	if e.BizStep != "" {
		e.BizStep, _ = Short("bizstep", e.BizStep)
	}
	if e.Disposition != "" {
		e.Disposition, _ = Short("disp", e.Disposition)
	}
	for i := range e.BizTransactionList {
		if e.BizTransactionList[i].Type != "" {
			e.BizTransactionList[i].Type, _ = Short("btt", e.BizTransactionList[i].Type)
		}
	}
	for i := range e.SourceList {
		e.SourceList[i].Type, _ = Short("sdt", e.SourceList[i].Type)
	}
	for i := range e.DestinationList {
		e.DestinationList[i].Type, _ = Short("sdt", e.DestinationList[i].Type)
	}
	if e.ErrorDeclaration != nil && e.ErrorDeclaration.Reason != "" {
		e.ErrorDeclaration.Reason, _ = Short("er", e.ErrorDeclaration.Reason)
	}
	if e.EventTimeZoneOffset == "" && !e.EventTime.IsZero() {
		e.EventTimeZoneOffset = e.EventTime.Format("-07:00")
	}
}

// Validate checks one event against EPCIS 2.0 structure rules and the CBV.
// Events should be normalized first.
func Validate(i int, e *Event) []FieldError {
	var errs []FieldError
	add := func(field, msg string) {
		errs = append(errs, FieldError{Index: i, EventID: e.EventID, Field: field, Message: msg})
	}
	if e.EventTime.IsZero() {
		add("eventTime", "required")
	}
	if !tzRe.MatchString(e.EventTimeZoneOffset) {
		add("eventTimeZoneOffset", "must be ±HH:MM")
	}
	needAction := func() {
		switch e.Action {
		case ActionAdd, ActionObserve, ActionDelete:
		case "":
			add("action", "required")
		default:
			add("action", "must be ADD, OBSERVE or DELETE")
		}
	}
	switch e.Type {
	case ObjectEvent:
		needAction()
		if len(e.EPCList) == 0 && len(e.QuantityList) == 0 {
			add("epcList", "epcList or quantityList is required")
		}
	case AggregationEvent:
		needAction()
		if e.Action != ActionObserve && e.ParentID == "" {
			add("parentID", "required for ADD and DELETE")
		}
		if e.Action != ActionDelete && len(e.ChildEPCs) == 0 && len(e.ChildQuantityList) == 0 {
			add("childEPCs", "childEPCs or childQuantityList is required")
		}
	case TransactionEvent:
		needAction()
		if len(e.BizTransactionList) == 0 {
			add("bizTransactionList", "required")
		}
	case TransformationEvent:
		if e.Action != "" {
			add("action", "not allowed on TransformationEvent")
		}
		if len(e.InputEPCList)+len(e.InputQuantityList)+len(e.OutputEPCList)+len(e.OutputQuantityList) == 0 && e.TransformationID == "" {
			add("inputEPCList", "inputs or outputs (or transformationID) are required")
		}
	case AssociationEvent:
		needAction()
		if e.ParentID == "" {
			add("parentID", "required")
		}
	case "":
		add("type", "required")
	default:
		add("type", "unknown event type "+e.Type)
	}
	if e.BizStep != "" {
		if v, cbv := Short("bizstep", e.BizStep); cbv && !BizSteps[v] {
			add("bizStep", "not a CBV business step: "+v)
		}
	}
	if e.Disposition != "" {
		if v, cbv := Short("disp", e.Disposition); cbv && !Dispositions[v] {
			add("disposition", "not a CBV disposition: "+v)
		}
	}
	for _, bt := range e.BizTransactionList {
		if bt.BizTransaction == "" {
			add("bizTransactionList", "bizTransaction is required")
		}
		if bt.Type != "" {
			if v, cbv := Short("btt", bt.Type); cbv && !BizTransactionTypes[v] {
				add("bizTransactionList", "not a CBV transaction type: "+v)
			}
		}
	}
	for _, s := range e.SourceList {
		if v, cbv := Short("sdt", s.Type); cbv && !SourceDestTypes[v] {
			add("sourceList", "not a CBV source type: "+v)
		}
	}
	for _, d := range e.DestinationList {
		if v, cbv := Short("sdt", d.Type); cbv && !SourceDestTypes[v] {
			add("destinationList", "not a CBV destination type: "+v)
		}
	}
	check := func(field string, list []string) {
		for _, id := range list {
			if msg := CheckEPC(id); msg != "" {
				add(field, msg)
			}
		}
	}
	check("epcList", e.EPCList)
	check("childEPCs", e.ChildEPCs)
	check("inputEPCList", e.InputEPCList)
	check("outputEPCList", e.OutputEPCList)
	if e.ParentID != "" {
		check("parentID", []string{e.ParentID})
	}
	for _, ql := range [][]QuantityElement{e.QuantityList, e.ChildQuantityList, e.InputQuantityList, e.OutputQuantityList} {
		for _, q := range ql {
			if q.EPCClass == "" {
				add("quantityList", "epcClass is required")
			}
			if q.Quantity != nil && *q.Quantity < 0 {
				add("quantityList", "quantity must be ≥ 0")
			}
		}
	}
	return errs
}

// CheckEPC validates an instance identifier (EPC URN or GS1 Digital Link) and returns a message or "".
func CheckEPC(id string) string {
	switch {
	case strings.HasPrefix(id, "urn:epc:id:sgtin:"):
		if _, _, _, err := gs1.ParseSGTINURN(id); err != nil {
			return err.Error() + ": " + id
		}
	case strings.HasPrefix(id, "urn:epc:id:sscc:"):
		if _, err := gs1.ParseSSCCURN(id); err != nil {
			return err.Error() + ": " + id
		}
	case strings.HasPrefix(id, "urn:epc:id:sgln:"):
		if _, err := gs1.ParseSGLNURN(id); err != nil {
			return err.Error() + ": " + id
		}
	case strings.HasPrefix(id, "urn:epc:"):
	case strings.HasPrefix(id, "http://"), strings.HasPrefix(id, "https://"):
		if gs1.IsDigitalLink(id) {
			ais, err := gs1.ParseDigitalLink(id)
			if err != nil {
				return err.Error() + ": " + id
			}
			for _, a := range ais {
				if a.Code == "01" && !gs1.ValidGTIN(a.Value) {
					return "invalid GTIN check digit: " + id
				}
				if a.Code == "00" && !gs1.ValidSSCC(a.Value) {
					return "invalid SSCC check digit: " + id
				}
			}
		}
	case strings.Contains(id, ":"):
	default:
		return "identifier must be a URI: " + id
	}
	return ""
}

// ValidateDocument normalizes and validates every event.
func ValidateDocument(d *Document) []FieldError {
	per := make([][]FieldError, len(d.Body.EventList))
	_ = ParallelFor(len(d.Body.EventList), func(i int) error {
		Normalize(&d.Body.EventList[i])
		per[i] = Validate(i, &d.Body.EventList[i])
		return nil
	})
	var errs []FieldError
	for _, e := range per {
		errs = append(errs, e...)
	}
	return errs
}
