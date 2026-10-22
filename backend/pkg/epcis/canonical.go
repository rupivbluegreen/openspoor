package epcis

import (
	"strings"

	"github.com/openspoor/openspoor/pkg/gs1"
)

// Canonical maps an instance or class identifier to one storage key so that the same
// object matches whether a partner sends an EPC URN or a GS1 Digital Link (on any domain).
// GS1 keys become https://id.gs1.org Digital Links holding only the key and its key
// qualifiers; other URIs are returned unchanged.
func Canonical(id string) string {
	switch {
	case strings.HasPrefix(id, "urn:epc:id:sgtin:"), strings.HasPrefix(id, "urn:epc:class:lgtin:"),
		strings.HasPrefix(id, "urn:epc:id:sscc:"), strings.HasPrefix(id, "urn:epc:id:sgln:"):
		if dl, err := gs1.URNToDigitalLink(id, gs1.DefaultResolver); err == nil {
			return dl
		}
	case strings.HasPrefix(id, "urn:epc:class:lgtin:") || strings.HasPrefix(id, "urn:epc:idpat:"):
		return id
	case gs1.IsDigitalLink(id):
		ais, err := gs1.ParseDigitalLink(id)
		if err != nil {
			return id
		}
		m := map[string]string{}
		for _, a := range ais {
			m[a.Code] = a.Value
		}
		switch {
		case m["01"] != "" && m["21"] != "":
			return gs1.DigitalLink(gs1.DefaultResolver, gs1.Pack{GTIN: m["01"], Serial: m["21"]})
		case m["01"] != "" && m["10"] != "":
			return gs1.DigitalLink(gs1.DefaultResolver, gs1.Pack{GTIN: m["01"], Lot: m["10"]})
		case m["00"] != "":
			return gs1.DefaultResolver + "/00/" + m["00"]
		case m["414"] != "":
			if m["254"] != "" && m["254"] != "0" {
				return gs1.DefaultResolver + "/414/" + m["414"] + "/254/" + m["254"]
			}
			return gs1.DefaultResolver + "/414/" + m["414"]
		case m["01"] != "":
			return gs1.DefaultResolver + "/01/" + m["01"]
		}
	}
	return id
}

// Key splits a canonical identifier into GTIN and serial (SGTIN), or GTIN and lot (LGTIN).
func Key(canonical string) (gtin, serial, lot string) {
	if !gs1.IsDigitalLink(canonical) {
		return "", "", ""
	}
	ais, err := gs1.ParseDigitalLink(canonical)
	if err != nil {
		return "", "", ""
	}
	for _, a := range ais {
		switch a.Code {
		case "01":
			gtin = a.Value
		case "21":
			serial = a.Value
		case "10":
			lot = a.Value
		}
	}
	return gtin, serial, lot
}

// CanonicalAll canonicalizes a list, dropping duplicates and empties.
func CanonicalAll(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		c := Canonical(id)
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	return out
}
