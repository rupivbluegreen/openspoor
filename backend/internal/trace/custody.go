package trace

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	lib "github.com/openspoor/openspoor/pkg/epcis"
	"github.com/openspoor/openspoor/pkg/gs1"
)

// Event categories used by the mass balance and the custody checks.
const (
	CatCommissioning   = "commissioning"
	CatShipping        = "shipping"
	CatReceiving       = "receiving"
	CatDecommissioning = "decommissioning"
	CatDestroying      = "destroying"
)

// Limits that keep a reconciliation bounded.
const (
	maxScopeSerials = 100000
	maxEventRows    = 1500000
	maxAggRounds    = 4
)

// evRow is the part of a stored EPCIS event the custody logic reads.
type evRow struct {
	ID          string
	Type        string
	Action      string
	BizStep     string
	Disposition string
	Location    string
	Parent      string
	Time        time.Time
	Recorded    time.Time
	EPCs        []string
	Dest        []string // destination party / location ids
	Via         bool     // reached through an aggregation parent, not listed for the serial itself
}

// classify maps an event to one balance category, or "" when it does not move custody.
// The business step decides; without one, a destroyed disposition or an ObjectEvent DELETE
// is read as destroying / decommissioning.
func classify(e evRow) string {
	if e.Type == lib.TransformationEvent {
		return ""
	}
	switch e.BizStep {
	case "commissioning":
		return CatCommissioning
	case "shipping":
		return CatShipping
	case "receiving":
		return CatReceiving
	case "decommissioning":
		return CatDecommissioning
	case "destroying":
		return CatDestroying
	}
	switch {
	case e.Disposition == "destroyed":
		return CatDestroying
	case e.Type == lib.ObjectEvent && e.Action == lib.ActionDelete:
		return CatDecommissioning
	}
	return ""
}

// glnOf extracts a GLN from an SGLN / PGLN URN or a GS1 Digital Link carrying key 414.
func glnOf(id string) string {
	id = strings.TrimSpace(id)
	for _, p := range []string{"urn:epc:id:sgln:", "urn:epc:id:pgln:"} {
		if rest, ok := strings.CutPrefix(id, p); ok {
			parts := strings.Split(rest, ".")
			if len(parts) < 2 || len(parts[0])+len(parts[1]) != 12 {
				return ""
			}
			return gs1.WithCheckDigit(parts[0] + parts[1])
		}
	}
	if gs1.IsDigitalLink(id) {
		if ais, err := gs1.ParseDigitalLink(id); err == nil {
			for _, a := range ais {
				if a.Code == "414" && gs1.ValidGLN(a.Value) {
					return a.Value
				}
			}
		}
	}
	if gs1.ValidGLN(id) {
		return id
	}
	return ""
}

type scopeSerial struct {
	EPC string
	Lot string
	// Status is the projected status (serial_state).
	Status string
}

// loadScope returns the serials of a GTIN (and lot, when given) known to the projection.
func loadScope(ctx context.Context, tx pgx.Tx, gtin, lot string) (out []scopeSerial, truncated bool, err error) {
	rows, err := tx.Query(ctx, `select epc, coalesce(lot,''), status from serial_state
		where tenant_id = app_tenant() and gtin=$1 and serial is not null and ($2='' or lot=$2) order by epc limit $3`, gtin, lot, maxScopeSerials+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var s scopeSerial
		if err := rows.Scan(&s.EPC, &s.Lot, &s.Status); err != nil {
			return nil, false, err
		}
		out = append(out, s)
	}
	if len(out) > maxScopeSerials {
		return out[:maxScopeSerials], true, rows.Err()
	}
	return out, false, rows.Err()
}

type destList struct {
	Destination []struct {
		Destination string `json:"destination"`
	} `json:"destination"`
}

// loadEvents reads the stored events that mention any of epcs, oldest first.
func loadEvents(ctx context.Context, tx pgx.Tx, epcs []string, limit int) ([]evRow, error) {
	rows, err := tx.Query(ctx, `select event_id, event_type, coalesce(action,''), coalesce(biz_step,''), coalesce(disposition,''),
			coalesce(biz_location,''), coalesce(parent_id,''), event_time, record_time, epcs, source_dest
		from epcis_event where tenant_id = app_tenant() and epcs && $1::text[] order by event_time, record_time, event_id limit $2`, epcs, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []evRow
	for rows.Next() {
		var e evRow
		var sd []byte
		if err := rows.Scan(&e.ID, &e.Type, &e.Action, &e.BizStep, &e.Disposition, &e.Location, &e.Parent, &e.Time, &e.Recorded, &e.EPCs, &sd); err != nil {
			return nil, err
		}
		if len(sd) > 0 {
			var dl destList
			if json.Unmarshal(sd, &dl) == nil {
				for _, d := range dl.Destination {
					e.Dest = append(e.Dest, d.Destination)
				}
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

type containment struct {
	serial   string
	from, to time.Time
}

var (
	farPast   = time.Unix(0, 0).UTC()
	farFuture = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
)

// attributeEvents assigns the events of a serial and of the aggregation parents that held it
// (at the time of the event) to the serial. fetch loads events for a set of identifiers; it is
// called once for the serials and once per aggregation level, so the logic is testable
// without a database.
func attributeEvents(serials []string, fetch func(epcs []string) ([]evRow, error)) (map[string][]evRow, error) {
	out := make(map[string][]evRow, len(serials))
	seen := map[string]map[string]bool{}
	add := func(serial string, e evRow, via bool) {
		m := seen[serial]
		if m == nil {
			m = map[string]bool{}
			seen[serial] = m
		}
		if m[e.ID] {
			return
		}
		m[e.ID] = true
		e.Via = via
		out[serial] = append(out[serial], e)
	}
	contain := map[string][]containment{}
	for _, s := range serials {
		contain[s] = []containment{{s, farPast, farFuture}}
	}
	frontier := append([]string(nil), serials...)
	handled := map[string]bool{}
	for round := 0; round < maxAggRounds && len(frontier) > 0; round++ {
		rows, err := fetch(frontier)
		if err != nil {
			return nil, err
		}
		inFrontier := make(map[string]bool, len(frontier))
		for _, f := range frontier {
			inFrontier[f] = true
			handled[f] = true
		}
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Time.Before(rows[j].Time) })
		// Unpacking a parent without listing children ends every containment in it at that time.
		for _, e := range rows {
			if e.Type == lib.AggregationEvent && e.Action == lib.ActionDelete && e.Parent != "" && len(e.EPCs) <= 1 && inFrontier[e.Parent] {
				for i := range contain[e.Parent] {
					if c := &contain[e.Parent][i]; c.serial != e.Parent && !c.from.After(e.Time) && c.to.After(e.Time) {
						c.to = e.Time
					}
				}
			}
		}
		type edge struct{ child, parent string }
		open := map[edge]time.Time{}
		closed := map[edge][][2]time.Time{}
		for _, e := range rows {
			for _, n := range e.EPCs {
				if !inFrontier[n] {
					continue
				}
				for _, c := range contain[n] {
					if !e.Time.Before(c.from) && !e.Time.After(c.to) {
						add(c.serial, e, round > 0)
					}
				}
			}
			if e.Type != lib.AggregationEvent || e.Parent == "" {
				continue
			}
			var kids []string
			for _, n := range e.EPCs {
				if n != e.Parent && inFrontier[n] {
					kids = append(kids, n)
				}
			}
			switch e.Action {
			case lib.ActionAdd, lib.ActionObserve:
				for _, k := range kids {
					if _, ok := open[edge{k, e.Parent}]; !ok {
						open[edge{k, e.Parent}] = e.Time
					}
				}
			case lib.ActionDelete:
				if len(kids) == 0 && len(e.EPCs) <= 1 { // unpack everything below the parent
					for ed, start := range open {
						if ed.parent == e.Parent {
							closed[ed] = append(closed[ed], [2]time.Time{start, e.Time})
							delete(open, ed)
						}
					}
				}
				for _, k := range kids {
					ed := edge{k, e.Parent}
					if start, ok := open[ed]; ok {
						closed[ed] = append(closed[ed], [2]time.Time{start, e.Time})
						delete(open, ed)
					}
				}
			}
		}
		next := map[string]bool{}
		grow := func(ed edge, start, end time.Time) {
			for _, c := range contain[ed.child] {
				from, to := c.from, c.to
				if start.After(from) {
					from = start
				}
				if end.Before(to) {
					to = end
				}
				if from.After(to) {
					continue
				}
				contain[ed.parent] = append(contain[ed.parent], containment{c.serial, from, to})
				if !handled[ed.parent] {
					next[ed.parent] = true
				}
			}
		}
		for ed, ivs := range closed {
			for _, iv := range ivs {
				grow(ed, iv[0], iv[1])
			}
		}
		for ed, start := range open {
			grow(ed, start, farFuture)
		}
		frontier = frontier[:0]
		for p := range next {
			frontier = append(frontier, p)
		}
		sort.Strings(frontier)
	}
	for s := range out {
		evs := out[s]
		sort.SliceStable(evs, func(i, j int) bool {
			if !evs[i].Time.Equal(evs[j].Time) {
				return evs[i].Time.Before(evs[j].Time)
			}
			if !evs[i].Recorded.Equal(evs[j].Recorded) {
				return evs[i].Recorded.Before(evs[j].Recorded)
			}
			return evs[i].ID < evs[j].ID
		})
	}
	return out, nil
}

// scopeEvents loads the serials of a GTIN/lot with the events that moved them.
func scopeEvents(ctx context.Context, tx pgx.Tx, gtin, lot string) (scope []scopeSerial, events map[string][]evRow, truncated bool, err error) {
	scope, truncated, err = loadScope(ctx, tx, gtin, lot)
	if err != nil || len(scope) == 0 {
		return scope, map[string][]evRow{}, truncated, err
	}
	epcs := make([]string, len(scope))
	for i, s := range scope {
		epcs[i] = s.EPC
	}
	budget := maxEventRows
	events, err = attributeEvents(epcs, func(ids []string) ([]evRow, error) {
		rows, err := loadEvents(ctx, tx, ids, budget)
		if len(rows) >= budget {
			truncated = true
		}
		budget -= len(rows)
		if budget < 1000 {
			budget = 1000
		}
		return rows, err
	})
	return scope, events, truncated, err
}
