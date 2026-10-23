package mockhub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// Hosts of the hubs that speak the placeholder JSON shape of package hubjson.
const (
	AEHost = "ae-tatmeen.mock.local"
	EGHost = "eg-eda.mock.local"
)

// JSONHub mocks a national hub whose interface specification is not public (Tatmeen, EDA).
// It accepts the placeholder report shape documented in package hubjson. Its checks only
// exercise the alert pipeline: they are mock behaviour (MOCK- codes), not regulatory rules,
// except the 20-character serial limit, which is GS1 AI (21).
type JSONHub struct {
	mu        sync.Mutex
	prefix    string
	issued    map[string]bool // gtin|serial commissioned
	results   map[string]*jsonResult
	seq       int
	lastAuth  string
	lastParty string
}

type jsonResult struct {
	Status       string              `json:"status"`
	ErrorCode    string              `json:"errorCode,omitempty"`
	ErrorMessage string              `json:"errorMessage,omitempty"`
	FailedItems  []map[string]string `json:"failedItems,omitempty"`
	polls        int
}

// NewJSONHub creates an empty hub; prefix starts report ids ("TTM", "EDA").
func NewJSONHub(prefix string) *JSONHub {
	return &JSONHub{prefix: prefix, issued: map[string]bool{}, results: map[string]*jsonResult{}}
}

// LastCaller returns the basic-auth user and participant header of the last request.
func (h *JSONHub) LastCaller() (user, participant string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastAuth, h.lastParty
}

type jsonReport struct {
	ReportType  string `json:"reportType"`
	ReceiverGLN string `json:"receiverGln"`
	Parent      string `json:"parent"`
	Items       []struct {
		GTIN, Serial, Batch, Expiry string
	} `json:"items"`
}

func (h *JSONHub) check(in jsonReport) *jsonResult {
	res := &jsonResult{Status: "ACCEPTED"}
	fail := func(code, msg string, items []map[string]string) *jsonResult {
		return &jsonResult{Status: "REJECTED", ErrorCode: code, ErrorMessage: msg, FailedItems: items}
	}
	var bad []map[string]string
	collect := func(pred func(gtin, serial, batch, expiry string) bool) []map[string]string {
		bad = nil
		for _, it := range in.Items {
			if pred(it.GTIN, it.Serial, it.Batch, it.Expiry) {
				bad = append(bad, map[string]string{"gtin": it.GTIN, "serial": it.Serial})
			}
		}
		return bad
	}
	switch in.ReportType {
	case "COMMISSION":
		if b := collect(func(_, s, _, _ string) bool { return len(s) > 20 }); len(b) > 0 {
			return fail("MOCK-SERIAL-TOO-LONG", "serial exceeds 20 characters (GS1 AI 21)", b)
		}
		if b := collect(func(_, _, l, e string) bool { return l == "" || e == "" }); len(b) > 0 {
			return fail("MOCK-BATCH-EXPIRY-MISSING", "batch and expiry are required on commissioning (mock rule)", b)
		}
		if b := collect(func(g, s, _, _ string) bool { return h.issued[g+"|"+s] }); len(b) > 0 {
			return fail("MOCK-SERIAL-DUPLICATE", "serial already commissioned", b)
		}
		for _, it := range in.Items {
			h.issued[it.GTIN+"|"+it.Serial] = true
		}
		return res
	case "AGGREGATION", "DISAGGREGATION":
		if in.Parent == "" {
			return fail("MOCK-PARENT-MISSING", "aggregation requires a parent (mock rule)", nil)
		}
	case "DISPATCH":
		if in.ReceiverGLN == "" {
			return fail("MOCK-RECEIVER-GLN-MISSING", "dispatch requires the receiver GLN (mock rule)", nil)
		}
	case "RECEIPT", "DECOMMISSION", "DESTRUCTION", "DISPENSE":
	default:
		return fail("MOCK-REPORT-TYPE-UNKNOWN", "unknown report type "+in.ReportType, nil)
	}
	if b := collect(func(g, s, _, _ string) bool { return !h.issued[g+"|"+s] }); len(b) > 0 {
		return fail("MOCK-SERIAL-UNKNOWN", "serials were never commissioned", b)
	}
	return res
}

func (h *JSONHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastAuth, _, _ = r.BasicAuth()
	h.lastParty = r.Header.Get("X-Participant-Id")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/reports":
		var in jsonReport
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		h.seq++
		id := fmt.Sprintf("%s-%06d", h.prefix, h.seq)
		h.results[id] = h.check(in)
		writeJSON(w, http.StatusAccepted, map[string]string{"reportId": id})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/reports/"):
		res, ok := h.results[strings.TrimPrefix(r.URL.Path, "/reports/")]
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown report"})
			return
		}
		res.polls++
		if res.polls == 1 { // asynchronous hub: the first poll is pending
			writeJSON(w, http.StatusOK, map[string]string{"status": "PENDING"})
			return
		}
		writeJSON(w, http.StatusOK, res)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}
