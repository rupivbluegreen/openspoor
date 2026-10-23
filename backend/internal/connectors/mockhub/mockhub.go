// Package mockhub provides in-memory mock regulatory hubs (EU Hub/EMVS, SFDA RSD, and
// placeholder-shape hubs for UAE Tatmeen and Egypt EDA) for development, demos and contract
// tests, plus an in-process SFTP server for integration tests.
// They are reachable through an in-process http.RoundTripper at http://<name>.mock.local,
// so no network or extra containers are needed. Error codes are prefixed MOCK- on purpose.
package mockhub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// Transport routes *.mock.local hosts to in-process handlers and everything else to Base.
type Transport struct {
	Base     http.RoundTripper
	Handlers map[string]http.Handler
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if h, ok := t.Handlers[r.URL.Host]; ok {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Result(), nil
	}
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(r)
}

// Hosts used by the "local-mock" connector setting.
const (
	EUHost = "eu-hub.mock.local"
	SAHost = "sa-rsd.mock.local"
)

// NewTransport returns a transport with every mock hub.
func NewTransport() *Transport {
	return &Transport{Handlers: map[string]http.Handler{EUHost: NewEUHub(), SAHost: NewSAHub(),
		AEHost: NewJSONHub("TTM"), EGHost: NewJSONHub("EDA")}}
}

type txn struct {
	Status      string     `json:"status"`
	Errors      []hubError `json:"errors"`
	ProductCode string     `json:"productCode"`
	Batch       string     `json:"batch"`
	polls       int
}

type hubError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Serial  string `json:"serial,omitempty"`
}

// EUHub mocks pack data upload and status updates.
type EUHub struct {
	mu       sync.Mutex
	products map[string]bool
	packs    map[string]string // gtin|serial -> state
	txns     map[string]*txn
	seq      int
}

// NewEUHub creates an empty hub. GTINs must be registered (POST /v1/products) before upload,
// except those registered automatically by AutoRegister.
func NewEUHub() *EUHub {
	return &EUHub{products: map[string]bool{}, packs: map[string]string{}, txns: map[string]*txn{}}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *EUHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/products":
		var in struct {
			ProductCode string `json:"productCode"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		h.products[in.ProductCode] = true
		writeJSON(w, http.StatusCreated, map[string]any{"registered": in.ProductCode})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/packdata":
		var in struct {
			ProductCode, Batch, Expiry string
			Serials                    []string
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		t := &txn{ProductCode: in.ProductCode, Batch: in.Batch}
		switch exp, err := time.Parse("2006-01-02", in.Expiry); {
		case !h.products[in.ProductCode]:
			t.Errors = append(t.Errors, hubError{"MOCK-PRODUCT-UNKNOWN", "product code " + in.ProductCode + " has no master data in the hub", ""})
		case in.Batch == "":
			t.Errors = append(t.Errors, hubError{"MOCK-BATCH-MISSING", "batch number is mandatory (2016/161 Art. 4(c))", ""})
		case err != nil || exp.Before(time.Now()):
			t.Errors = append(t.Errors, hubError{"MOCK-EXPIRY-INVALID", "expiry date missing, malformed or in the past", ""})
		default:
			for _, s := range in.Serials {
				if len(s) > 20 {
					t.Errors = append(t.Errors, hubError{"MOCK-SERIAL-TOO-LONG", "serial exceeds 20 characters (2016/161 Art. 4(b))", s})
				} else if _, dup := h.packs[in.ProductCode+"|"+s]; dup {
					t.Errors = append(t.Errors, hubError{"MOCK-SERIAL-DUPLICATE", "serial already uploaded", s})
				}
			}
		}
		if len(t.Errors) == 0 {
			for _, s := range in.Serials {
				h.packs[in.ProductCode+"|"+s] = "ACTIVE"
			}
			t.Status = "PROCESSED"
		} else {
			t.Status = "REJECTED"
		}
		h.seq++
		id := fmt.Sprintf("EUTX-%06d", h.seq)
		h.txns[id] = t
		writeJSON(w, http.StatusAccepted, map[string]string{"transactionId": id})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/packstatus":
		var in struct {
			ProductCode, Batch, NewState string
			Serials                      []string
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		t := &txn{ProductCode: in.ProductCode, Batch: in.Batch, Status: "PROCESSED"}
		for _, s := range in.Serials {
			if _, ok := h.packs[in.ProductCode+"|"+s]; !ok {
				t.Errors = append(t.Errors, hubError{"MOCK-SERIAL-UNKNOWN", "no pack data for serial", s})
			}
		}
		if len(t.Errors) > 0 {
			t.Status = "REJECTED"
		} else {
			for _, s := range in.Serials {
				h.packs[in.ProductCode+"|"+s] = in.NewState
			}
		}
		h.seq++
		id := fmt.Sprintf("EUTX-%06d", h.seq)
		h.txns[id] = t
		writeJSON(w, http.StatusAccepted, map[string]string{"transactionId": id})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/transactions/"):
		t, ok := h.txns[strings.TrimPrefix(r.URL.Path, "/v1/transactions/")]
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown transaction"})
			return
		}
		t.polls++
		if t.polls == 1 { // first poll is always pending, like an asynchronous hub
			writeJSON(w, http.StatusOK, map[string]any{"status": "PENDING"})
			return
		}
		writeJSON(w, http.StatusOK, t)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/packs":
		writeJSON(w, http.StatusOK, map[string]any{"state": h.packs[r.URL.Query().Get("productCode")+"|"+r.URL.Query().Get("serial")]})
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

// SAHub mocks RSD notifications.
type SAHub struct {
	mu       sync.Mutex
	produced map[string]bool
	results  map[string]map[string]any
	seq      int
}

// NewSAHub creates an empty RSD mock.
func NewSAHub() *SAHub {
	return &SAHub{produced: map[string]bool{}, results: map[string]map[string]any{}}
}

func (h *SAHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/notifications":
		var n struct {
			Type     string `json:"notificationType"`
			ToGLN    string `json:"toGln"`
			Products []struct {
				GTIN, Batch, Expiry string
				Serials             []string
			} `json:"products"`
		}
		if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		res := map[string]any{"result": "SUCCESS"}
		var failed []map[string]string
		switch n.Type {
		case "PRODUCTION":
			for _, p := range n.Products {
				for _, s := range p.Serials {
					h.produced[p.GTIN+"|"+s] = true
				}
			}
		case "DISPATCH":
			if n.ToGLN == "" {
				res = map[string]any{"result": "FAILED", "reasonCode": "MOCK-RECEIVER-GLN-MISSING", "description": "dispatch requires the receiver GLN"}
				break
			}
			fallthrough
		default:
			for _, p := range n.Products {
				for _, s := range p.Serials {
					if !h.produced[p.GTIN+"|"+s] {
						failed = append(failed, map[string]string{"GTIN": p.GTIN, "Serial": s})
					}
				}
			}
			if len(failed) > 0 {
				res = map[string]any{"result": "FAILED", "reasonCode": "MOCK-NOT-PRODUCED", "description": "serials were never notified as produced", "failedSerials": failed}
			}
		}
		h.seq++
		id := fmt.Sprintf("RSD-%06d", h.seq)
		h.results[id] = res
		writeJSON(w, http.StatusOK, map[string]string{"notificationId": id})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/notifications/"):
		res, ok := h.results[strings.TrimPrefix(r.URL.Path, "/notifications/")]
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown notification"})
			return
		}
		writeJSON(w, http.StatusOK, res)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

// RegisterEUProduct registers a GTIN in the EU mock hub (used by demo seed and tests).
func (t *Transport) RegisterEUProduct(gtin string) {
	if h, ok := t.Handlers[EUHost].(*EUHub); ok {
		h.mu.Lock()
		h.products[gtin] = true
		h.mu.Unlock()
	}
}
