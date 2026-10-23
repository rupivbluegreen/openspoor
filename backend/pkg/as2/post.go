package as2

import (
	"bytes"
	"context"
	"io"
	"net/http"
)

// Post sends a message or receipt to url and returns the response status, headers and body
// (bounded by MaxBody).
func Post(ctx context.Context, c *http.Client, url string, w *Wire) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(w.Body))
	if err != nil {
		return 0, nil, nil, err
	}
	for k, v := range w.Header {
		req.Header.Set(k, v)
	}
	res, err := c.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, MaxBody))
	return res.StatusCode, res.Header, b, err
}
