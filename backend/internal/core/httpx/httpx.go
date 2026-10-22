// Package httpx holds JSON and error helpers shared by every HTTP module.
package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/openspoor/openspoor/internal/core/db"
)

// Problem is an RFC 9457 problem document.
type Problem struct {
	Type   string `json:"type,omitempty"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
	Errors any    `json:"errors,omitempty"`
}

// Error is an error carrying an HTTP status.
type Error struct {
	Status int
	Msg    string
	Extra  any
}

func (e *Error) Error() string { return e.Msg }

// BadRequest builds a 400 error.
func BadRequest(msg string) error { return &Error{Status: http.StatusBadRequest, Msg: msg} }

// Unprocessable builds a 422 error with details.
func Unprocessable(msg string, details any) error {
	return &Error{Status: http.StatusUnprocessableEntity, Msg: msg, Extra: details}
}

// Forbidden builds a 403 error.
func Forbidden(msg string) error { return &Error{Status: http.StatusForbidden, Msg: msg} }

// Unauthorized builds a 401 error.
func Unauthorized(msg string) error { return &Error{Status: http.StatusUnauthorized, Msg: msg} }

// NotFound builds a 404 error.
func NotFound(msg string) error { return &Error{Status: http.StatusNotFound, Msg: msg} }

// Conflict builds a 409 error.
func Conflict(msg string) error { return &Error{Status: http.StatusConflict, Msg: msg} }

// TooMany builds a 429 error.
func TooMany(msg string) error { return &Error{Status: http.StatusTooManyRequests, Msg: msg} }

// JSON writes v as JSON with the given status.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// Fail writes err as a problem document.
func Fail(w http.ResponseWriter, r *http.Request, err error) {
	var he *Error
	status, msg := http.StatusInternalServerError, "internal error"
	var extra any
	switch {
	case errors.As(err, &he):
		status, msg, extra = he.Status, he.Msg, he.Extra
	case errors.Is(err, db.ErrNotFound):
		status, msg = http.StatusNotFound, "not found"
	case errors.Is(err, db.ErrConflict):
		status, msg = http.StatusConflict, err.Error()
	default:
		slog.ErrorContext(r.Context(), "request failed", "path", r.URL.Path, "err", err)
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Problem{Title: msg, Status: status, Errors: extra})
}

// Decode reads a JSON body (max 32 MiB) into v, rejecting unknown fields.
func Decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 32<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return BadRequest("invalid JSON body: " + err.Error())
	}
	return nil
}

// Handler adapts a func returning an error into an http.HandlerFunc.
func Handler(fn func(w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			Fail(w, r, err)
		}
	}
}

// IntQuery parses an integer query parameter with bounds.
func IntQuery(r *http.Request, key string, def, min, max int) int {
	v, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil {
		return def
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// Page is a generic list response.
type Page[T any] struct {
	Items []T `json:"items"`
	Total int `json:"total"`
}
