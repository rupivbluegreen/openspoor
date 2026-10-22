package epcis

import "strings"

// CBV 2.0 standard vocabularies (GS1 Core Business Vocabulary).
var (
	BizSteps = set("accepting", "arriving", "assembling", "collecting", "commissioning", "consigning", "creating_class_instance",
		"cycle_counting", "decommissioning", "departing", "destroying", "disassembling", "dispensing", "encoding", "entering_exiting",
		"holding", "inspecting", "installing", "killing", "loading", "other", "packing", "picking", "receiving", "removing",
		"repackaging", "repairing", "replacing", "reserving", "retail_selling", "sampling", "sensor_reporting", "shipping",
		"staging_outbound", "stock_taking", "stocking", "storing", "transporting", "unloading", "unpacking", "void_shipping")
	Dispositions = set("active", "available", "completeness_verified", "completeness_inferred", "conformant", "container_closed",
		"container_open", "damaged", "destroyed", "dispensed", "disposed", "encoded", "expired", "in_progress", "in_transit",
		"inactive", "mismatch_instance", "mismatch_class", "mismatch_quantity", "needs_replacement", "no_pedigree_match",
		"non_conformant", "non_sellable_other", "partially_dispensed", "recalled", "reserved", "retail_sold", "returned",
		"sellable_accessible", "sellable_not_accessible", "stolen", "unavailable", "unknown")
	BizTransactionTypes = set("bol", "cert", "desadv", "inv", "pedigree", "po", "poc", "prodorder", "recadv", "rma", "testprd", "testres", "upevt")
	SourceDestTypes     = set("owning_party", "possessing_party", "location")
	ErrorReasons        = set("did_not_occur", "incorrect_data")
)

func set(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

var cbvPrefixes = map[string][]string{
	"bizstep":     {"urn:epcglobal:cbv:bizstep:", "https://ref.gs1.org/cbv/BizStep-", "cbv:BizStep-"},
	"disp":        {"urn:epcglobal:cbv:disp:", "https://ref.gs1.org/cbv/Disp-", "cbv:Disp-"},
	"btt":         {"urn:epcglobal:cbv:btt:", "https://ref.gs1.org/cbv/BTT-", "cbv:BTT-"},
	"sdt":         {"urn:epcglobal:cbv:sdt:", "https://ref.gs1.org/cbv/SDT-", "cbv:SDT-"},
	"er":          {"urn:epcglobal:cbv:er:", "https://ref.gs1.org/cbv/ER-", "cbv:ER-"},
}

// Short normalizes a CBV value to its bare term (e.g. "shipping"). Non-CBV URIs are returned unchanged.
// The bool reports whether the value is in CBV form (bare or one of the CBV prefixes).
func Short(kind, v string) (string, bool) {
	for _, p := range cbvPrefixes[kind] {
		if strings.HasPrefix(v, p) {
			return v[len(p):], true
		}
	}
	if !strings.Contains(v, ":") {
		return v, true
	}
	return v, false
}

// URN returns the EPCIS 1.2/XML URN form of a bare CBV term.
func URN(kind, v string) string {
	if v == "" || strings.Contains(v, ":") {
		return v
	}
	return cbvPrefixes[kind][0] + v
}
