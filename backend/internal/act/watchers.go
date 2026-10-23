package act

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Watchers are deterministic rules over recent events and scans that flag counterfeit and
// diversion signals. They only propose cases for a person to review; they never change a
// record. Thresholds are fixed and documented here so the rules can be validated.

const (
	spikeMinCount  = 5  // failed or reported scans in a day before a GTIN can spike
	spikeFactor    = 3  // ... and at least this many times its recent daily average
	spikeBaseDays  = 14 // days of history for the average
	reappearWindow = 7  // days of new events checked for reappeared serials
	duplicateDays  = 30 // days of verification requests checked for duplicates
)

// terminalDispositions end a pack's life; any later event for the same serial is a signal.
var terminalDispositions = []string{"destroyed", "dispensed", "disposed", "stolen"}

type verificationSpike struct{}

func (verificationSpike) Name() string { return "verification-spike" }
func (verificationSpike) Description() string {
	return fmt.Sprintf("Opens a case when failed or reported public scans of a product reach %d in a day and %dx its %d-day average.",
		spikeMinCount, spikeFactor, spikeBaseDays)
}
func (verificationSpike) Observe(ctx context.Context, tx pgx.Tx) ([]Proposal, error) {
	rows, err := tx.Query(ctx, `with daily as (
			select gtin, day, sum(count) as n from scan_stat
			where tenant_id = app_tenant() and outcome in ('not_found','suspect','decommissioned','recalled','expired','reported')
			  and day >= current_date - $1::int group by gtin, day)
		select gtin, coalesce(sum(n) filter (where day = current_date), 0)::bigint as today,
		       coalesce(sum(n) filter (where day < current_date), 0)::float8 / $1 as baseline
		from daily group by gtin`, spikeBaseDays)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Proposal
	today := time.Now().UTC().Format(time.DateOnly)
	for rows.Next() {
		var gtin string
		var n int64
		var base float64
		if err := rows.Scan(&gtin, &n, &base); err != nil {
			return nil, err
		}
		if n < spikeMinCount || float64(n) < spikeFactor*max(base, 1) {
			continue
		}
		title := fmt.Sprintf("Spike in failed scans of GTIN %s: %d today", gtin, n)
		out = append(out, Proposal{Tool: "case.open", Title: title,
			Summary: "Public scans that failed verification or were reported rose sharply. Counterfeit or diverted packs may be on the market.",
			Reasoning: []string{fmt.Sprintf("%d failed or reported scans today; the %d-day average is %.1f a day.", n, spikeBaseDays, base),
				fmt.Sprintf("Rule: at least %d and at least %dx the average.", spikeMinCount, spikeFactor)},
			Input:   caseInput{Kind: "watch.verification_spike", Title: title, Priority: 1, Entity: "product", EntityID: gtin, Source: "watcher", SourceID: "verification-spike"},
			Preview: map[string]any{"priority": 1}, Confidence: 0.7, Entity: "product", EntityID: gtin, DedupeKey: "spike:" + gtin + ":" + today})
	}
	return out, rows.Err()
}

type reappearedSerial struct{}

func (reappearedSerial) Name() string { return "reappeared-serial" }
func (reappearedSerial) Description() string {
	return "Opens a case when a pack has new events after it was dispensed, destroyed, disposed of or reported stolen: a duplicate or counterfeit copy is likely."
}
func (reappearedSerial) Observe(ctx context.Context, tx pgx.Tx) ([]Proposal, error) {
	rows, err := tx.Query(ctx, `with recent as (
			select unnest(epcs) as epc, event_time, coalesce(biz_step,'') as biz_step, coalesce(biz_location,'') as loc
			from epcis_event where tenant_id = app_tenant() and record_time > now() - make_interval(days => $1)),
		ended as (
			select e.epc, min(v.event_time) as at, min(v.disposition) as disposition
			from (select distinct epc from recent) e
			join epcis_event v on v.tenant_id = app_tenant() and v.epcs @> array[e.epc] and v.disposition = any($2)
			group by e.epc)
		select r.epc, d.disposition, d.at, r.event_time, r.biz_step, r.loc
		from recent r join ended d on d.epc = r.epc
		where r.event_time > d.at order by r.event_time limit 50`, reappearWindow, terminalDispositions)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Proposal
	for rows.Next() {
		var epc, disp, step, loc string
		var ended, seen time.Time
		if err := rows.Scan(&epc, &disp, &ended, &seen, &step, &loc); err != nil {
			return nil, err
		}
		title := "Pack seen after it was " + disp
		reason := []string{fmt.Sprintf("%s on %s.", strings.ToUpper(disp[:1])+disp[1:], ended.UTC().Format(time.DateOnly)),
			fmt.Sprintf("New event %s on %s.", nonEmpty(step, "observation"), seen.UTC().Format(time.DateOnly))}
		if loc != "" {
			reason = append(reason, "Location: "+loc+".")
		}
		out = append(out, Proposal{Tool: "case.open", Title: title + ": " + epc,
			Summary:   "A pack cannot move after the end of its life. Quarantine it and open an investigation.",
			Reasoning: reason,
			Input:     caseInput{Kind: "watch.reappeared_serial", Title: title + ": " + epc, Priority: 1, Entity: "serial", EntityID: epc, Source: "watcher", SourceID: "reappeared-serial"},
			Preview:   map[string]any{"priority": 1}, Confidence: 0.85, Entity: "serial", EntityID: epc, DedupeKey: "reappeared:" + epc})
	}
	return out, rows.Err()
}

type duplicateVerification struct{}

func (duplicateVerification) Name() string { return "duplicate-verification" }
func (duplicateVerification) Description() string {
	return fmt.Sprintf("Opens a case when the same serial number is verified by two or more different trading partners within %d days.", duplicateDays)
}
func (duplicateVerification) Observe(ctx context.Context, tx pgx.Tx) ([]Proposal, error) {
	rows, err := tx.Query(ctx, `select gtin, serial, array_agg(distinct requestor_gln order by requestor_gln) from vrs_request
		where tenant_id = app_tenant() and direction = 'inbound' and requestor_gln <> '' and created_at > now() - make_interval(days => $1)
		group by gtin, serial having count(distinct requestor_gln) >= 2 limit 50`, duplicateDays)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Proposal
	for rows.Next() {
		var gtin, serial string
		var glns []string
		if err := rows.Scan(&gtin, &serial, &glns); err != nil {
			return nil, err
		}
		title := fmt.Sprintf("Serial %s of GTIN %s verified by %d partners", serial, gtin, len(glns))
		out = append(out, Proposal{Tool: "case.open", Title: title,
			Summary:   "One pack can be with one partner at a time. Copies of this serial number may be in circulation.",
			Reasoning: []string{"Requesting GLNs: " + strings.Join(glns, ", ") + "."},
			Input:     caseInput{Kind: "watch.duplicate_verification", Title: title, Priority: 2, Entity: "serial", EntityID: gtin + "/" + serial, Source: "watcher", SourceID: "duplicate-verification"},
			Preview:   map[string]any{"priority": 2}, Confidence: 0.75, Entity: "serial", EntityID: gtin + "/" + serial, DedupeKey: "dupverify:" + gtin + "/" + serial})
	}
	return out, rows.Err()
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
