// Package appsdk helps third parties build Open Spoor apps and agents: verify signed EPCIS
// webhook deliveries, call the REST API with an app key, and describe the app in a manifest
// for the marketplace. It depends only on the standard library.
package appsdk

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Manifest is the marketplace description of an app (see POST /api/v1/apps).
type Manifest struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Kind        string   `json:"kind"` // connector | agent | rulepack | integration | app
	Publisher   string   `json:"publisher"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Roles       []string `json:"roles"`
	Webhook     string   `json:"webhook,omitempty"`
	Homepage    string   `json:"homepage,omitempty"`
}

// VerifyWebhook checks the X-OpenSpoor-Signature header ("sha256=<hex>") of an EPCIS
// subscription delivery in constant time.
func VerifyWebhook(secret string, body []byte, header string) bool {
	got, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	sig, err := hex.DecodeString(got)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(sig, mac.Sum(nil))
}

// WebhookHandler verifies deliveries and passes the EPCIS query document to fn.
func WebhookHandler(secret string, fn func(ctx context.Context, doc json.RawMessage) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
		if err != nil || !VerifyWebhook(secret, body, r.Header.Get("X-OpenSpoor-Signature")) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		if err := fn(r.Context(), body); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError) // Open Spoor retries with backoff
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// Client calls the Open Spoor API with an app API key.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// New creates a client for e.g. https://acme.openspoor.example.
func New(baseURL, apiKey string) *Client {
	return &Client{BaseURL: strings.TrimSuffix(baseURL, "/"), APIKey: apiKey, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// APIError is a problem+json error.
type APIError struct {
	Status int             `json:"status"`
	Title  string          `json:"title"`
	Errors json.RawMessage `json:"errors,omitempty"`
}

func (e *APIError) Error() string { return fmt.Sprintf("openspoor: %d %s", e.Status, e.Title) }

// Do sends a request; body may be nil, []byte or a JSON-encodable value; out may be nil.
func (c *Client) Do(ctx context.Context, method, path string, body, out any, header ...string) error {
	var rd io.Reader
	ct := "application/json"
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	default:
		buf, err := json.Marshal(b)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+"/api/v1"+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", ct)
	req.Header.Set("X-API-Key", c.APIKey)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 256<<20))
	if res.StatusCode >= 300 {
		e := &APIError{Status: res.StatusCode}
		_ = json.Unmarshal(data, e)
		return e
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// CaptureResult is the response of Capture.
type CaptureResult struct {
	CaptureID  string `json:"captureId"`
	Status     string `json:"status"`
	EventCount int    `json:"eventCount"`
}

// Capture submits an EPCIS 2.0 JSON-LD or XML document.
func (c *Client) Capture(ctx context.Context, doc []byte) (CaptureResult, error) {
	var r CaptureResult
	return r, c.Do(ctx, http.MethodPost, "/epcis/capture", doc, &r)
}

// Allocate reserves serial numbers idempotently.
func (c *Client) Allocate(ctx context.Context, poolID string, quantity int, idempotencyKey string) ([]string, error) {
	if idempotencyKey == "" {
		return nil, errors.New("idempotency key is required")
	}
	var r struct {
		Serials []string `json:"serials"`
	}
	err := c.Do(ctx, http.MethodPost, "/serial-pools/"+url.PathEscape(poolID)+"/allocate", map[string]int{"quantity": quantity}, &r, "Idempotency-Key", idempotencyKey)
	return r.Serials, err
}

// Events runs an EPCIS query (REST binding parameters) and returns the raw query document.
func (c *Client) Events(ctx context.Context, params url.Values) (json.RawMessage, error) {
	var r json.RawMessage
	return r, c.Do(ctx, http.MethodGet, "/epcis/events?"+params.Encode(), nil, &r)
}
