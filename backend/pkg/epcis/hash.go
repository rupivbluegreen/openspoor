package epcis

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// HashID derives a deterministic event identifier from the event's business content,
// so the same event captured twice gets the same ID (idempotent capture).
//
// TODO(spec): this follows the structure of the GS1 CBV 2.0 event hash (ordered
// pre-hash string of normalized fields) but is not yet byte-for-byte the GS1
// canonical algorithm; it is therefore tagged ver=openspoor1, not ver=CBV2.0.
func HashID(e *Event) string {
	var b strings.Builder
	w := func(k, v string) {
		if v != "" {
			b.WriteString(k)
			b.WriteString("=")
			b.WriteString(v)
		}
	}
	list := func(k string, xs []string) {
		if len(xs) == 0 {
			return
		}
		s := append([]string(nil), xs...)
		sort.Strings(s)
		w(k, strings.Join(s, ","))
	}
	w("eventType", e.Type)
	w("eventTime", e.EventTime.UTC().Format(time.RFC3339Nano))
	w("eventTimeZoneOffset", e.EventTimeZoneOffset)
	w("parentID", e.ParentID)
	list("epcList", e.EPCList)
	list("childEPCs", e.ChildEPCs)
	list("inputEPCList", e.InputEPCList)
	list("outputEPCList", e.OutputEPCList)
	for _, ql := range []struct {
		k string
		v []QuantityElement
	}{{"quantityList", e.QuantityList}, {"childQuantityList", e.ChildQuantityList}, {"inputQuantityList", e.InputQuantityList}, {"outputQuantityList", e.OutputQuantityList}} {
		var xs []string
		for _, q := range ql.v {
			s := q.EPCClass
			if q.Quantity != nil {
				s += fmt.Sprintf("|%g|%s", *q.Quantity, q.UOM)
			}
			xs = append(xs, s)
		}
		list(ql.k, xs)
	}
	w("action", e.Action)
	w("bizStep", e.BizStep)
	w("disposition", e.Disposition)
	if e.ReadPoint != nil {
		w("readPoint", e.ReadPoint.ID)
	}
	if e.BizLocation != nil {
		w("bizLocation", e.BizLocation.ID)
	}
	var bts []string
	for _, t := range e.BizTransactionList {
		bts = append(bts, t.Type+"|"+t.BizTransaction)
	}
	list("bizTransactionList", bts)
	var srcs []string
	for _, s := range e.SourceList {
		srcs = append(srcs, s.Type+"|"+s.Source)
	}
	list("sourceList", srcs)
	var dsts []string
	for _, d := range e.DestinationList {
		dsts = append(dsts, d.Type+"|"+d.Destination)
	}
	list("destinationList", dsts)
	w("transformationID", e.TransformationID)
	sum := sha256.Sum256([]byte(b.String()))
	return "ni:///sha-256;" + hex.EncodeToString(sum[:]) + "?ver=openspoor1"
}
