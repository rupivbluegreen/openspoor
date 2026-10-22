package epcis

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
	"github.com/openspoor/openspoor/internal/core/jobs"
	lib "github.com/openspoor/openspoor/pkg/epcis"
)

// Query holds EPCIS 2.0 query parameters (REST binding names).
type Query map[string]string

var supported = map[string]bool{"eventType": true, "eventTypes": true, "GE_eventTime": true, "LT_eventTime": true, "GE_recordTime": true, "LT_recordTime": true,
	"EQ_action": true, "EQ_bizStep": true, "EQ_disposition": true, "EQ_readPoint": true, "EQ_bizLocation": true, "MATCH_epc": true, "MATCH_parentID": true,
	"MATCH_inputEPC": true, "MATCH_outputEPC": true, "MATCH_anyEPC": true, "MATCH_epcClass": true, "EQ_eventID": true, "EQ_captureID": true,
	"orderBy": true, "orderDirection": true, "perPage": true, "nextPageToken": true}

// FromValues builds a Query from URL values, rejecting unknown parameters.
func FromValues(v url.Values) (Query, error) {
	q := Query{}
	for k, vs := range v {
		if !supported[k] {
			return nil, httpx.BadRequest("unsupported query parameter: " + k)
		}
		q[k] = strings.Join(vs, ",")
	}
	return q, nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// sql translates the query into a WHERE clause and args.
func (q Query) sql() (string, []any, error) {
	var where []string
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	tm := func(k string) (time.Time, error) {
		t, err := time.Parse(time.RFC3339Nano, q[k])
		if err != nil {
			return t, httpx.BadRequest(k + " must be RFC 3339")
		}
		return t, nil
	}
	for k, v := range q {
		if v == "" {
			continue
		}
		switch k {
		case "eventType", "eventTypes":
			where = append(where, "event_type = any("+arg(splitList(v))+")")
		case "GE_eventTime", "LT_eventTime", "GE_recordTime", "LT_recordTime":
			t, err := tm(k)
			if err != nil {
				return "", nil, err
			}
			col := map[string]string{"GE_eventTime": "event_time >= ", "LT_eventTime": "event_time < ", "GE_recordTime": "record_time >= ", "LT_recordTime": "record_time < "}[k]
			where = append(where, col+arg(t))
		case "EQ_action":
			where = append(where, "action = any("+arg(splitList(v))+")")
		case "EQ_bizStep":
			var xs []string
			for _, s := range splitList(v) {
				x, _ := lib.Short("bizstep", s)
				xs = append(xs, x)
			}
			where = append(where, "biz_step = any("+arg(xs)+")")
		case "EQ_disposition":
			var xs []string
			for _, s := range splitList(v) {
				x, _ := lib.Short("disp", s)
				xs = append(xs, x)
			}
			where = append(where, "disposition = any("+arg(xs)+")")
		case "EQ_readPoint":
			where = append(where, "read_point = any("+arg(lib.CanonicalAll(splitList(v)))+")")
		case "EQ_bizLocation":
			where = append(where, "biz_location = any("+arg(lib.CanonicalAll(splitList(v)))+")")
		case "EQ_eventID":
			where = append(where, "event_id = any("+arg(splitList(v))+")")
		case "EQ_captureID":
			where = append(where, "capture_id = "+arg(v))
		case "MATCH_parentID":
			where = append(where, "parent_id = any("+arg(lib.CanonicalAll(splitList(v)))+")")
		case "MATCH_epc", "MATCH_anyEPC", "MATCH_inputEPC", "MATCH_outputEPC", "MATCH_epcClass":
			var exact, patterns []string
			for _, s := range splitList(v) {
				if strings.Contains(s, "*") {
					patterns = append(patterns, strings.ReplaceAll(strings.ReplaceAll(s, "%", `\%`), "*", "%"))
				} else {
					exact = append(exact, lib.Canonical(s))
				}
			}
			var ors []string
			if len(exact) > 0 {
				ors = append(ors, "epcs && "+arg(exact))
			}
			for _, p := range patterns {
				ors = append(ors, "exists (select 1 from unnest(epcs) x where x like "+arg(p)+")")
			}
			where = append(where, "("+strings.Join(ors, " or ")+")")
		}
	}
	if len(where) == 0 {
		return "true", args, nil
	}
	return strings.Join(where, " and "), args, nil
}

type pageToken struct {
	T  time.Time `json:"t"`
	ID uuid.UUID `json:"i"`
}

// Run executes the query and returns events plus the next page token.
func (q Query) Run(ctx context.Context, tx pgx.Tx) ([]lib.Event, string, error) {
	where, args, err := q.sql()
	if err != nil {
		return nil, "", err
	}
	per := 100
	if v := q["perPage"]; v != "" {
		fmt.Sscan(v, &per)
	}
	if per < 1 || per > 1000 {
		per = 100
	}
	col := "event_time"
	if q["orderBy"] == "recordTime" {
		col = "record_time"
	}
	dir, cmp := "asc", ">"
	if strings.EqualFold(q["orderDirection"], "DESC") {
		dir, cmp = "desc", "<"
	}
	if tok := q["nextPageToken"]; tok != "" {
		b, err := base64.RawURLEncoding.DecodeString(tok)
		var pt pageToken
		if err != nil || json.Unmarshal(b, &pt) != nil {
			return nil, "", httpx.BadRequest("invalid nextPageToken")
		}
		args = append(args, pt.T, pt.ID)
		where += fmt.Sprintf(" and (%s, id) %s ($%d, $%d)", col, cmp, len(args)-1, len(args))
	}
	args = append(args, per+1)
	rows, err := tx.Query(ctx, fmt.Sprintf(`select id, %s, doc from epcis_event where %s order by %s %s, id %s limit $%d`, col, where, col, dir, dir, len(args)), args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []lib.Event
	var last pageToken
	for rows.Next() {
		var id uuid.UUID
		var t time.Time
		var doc []byte
		if err := rows.Scan(&id, &t, &doc); err != nil {
			return nil, "", err
		}
		if len(out) == per {
			b, _ := json.Marshal(last)
			return out, base64.RawURLEncoding.EncodeToString(b), nil
		}
		var e lib.Event
		if err := json.Unmarshal(doc, &e); err != nil {
			return nil, "", err
		}
		out = append(out, e)
		last = pageToken{T: t, ID: id}
	}
	return out, "", rows.Err()
}

func queryDocument(name string, events []lib.Event) map[string]any {
	if events == nil {
		events = []lib.Event{}
	}
	return map[string]any{
		"@context": []string{lib.DefaultContext}, "type": "EPCISQueryDocument", "schemaVersion": "2.0",
		"creationDate": time.Now().UTC().Format(time.RFC3339Nano),
		"epcisBody":    map[string]any{"queryResults": map[string]any{"queryName": name, "resultsBody": map[string]any{"eventList": events}}},
	}
}

func (m *Module) writeQuery(w http.ResponseWriter, r *http.Request, name string, q Query) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		events, next, err := q.Run(r.Context(), tx)
		if err != nil {
			return err
		}
		if next != "" {
			v := r.URL.Query()
			v.Set("nextPageToken", next)
			w.Header().Set("Link", fmt.Sprintf(`<%s?%s>; rel="next"`, r.URL.Path, v.Encode()))
			w.Header().Set("GS1-Next-Page-Token", next)
		}
		if strings.Contains(r.Header.Get("Accept"), "xml") {
			b, err := lib.WriteXML(lib.NewDocument(events))
			if err != nil {
				return err
			}
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write(b)
			return nil
		}
		httpx.JSON(w, http.StatusOK, queryDocument(name, events))
		return nil
	})
}

func (m *Module) queryEvents(w http.ResponseWriter, r *http.Request) error {
	q, err := FromValues(r.URL.Query())
	if err != nil {
		return err
	}
	return m.writeQuery(w, r, "SimpleEventQuery", q)
}

func (m *Module) getEvent(w http.ResponseWriter, r *http.Request) error {
	id, _ := url.PathUnescape(chi.URLParam(r, "eventID"))
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		var doc json.RawMessage
		if err := tx.QueryRow(r.Context(), `select doc from epcis_event where event_id=$1 limit 1`, id).Scan(&doc); err != nil {
			return db.MapErr(err)
		}
		w.Header().Set("Content-Type", "application/ld+json")
		_, _ = w.Write(doc)
		return nil
	})
}

type namedQuery struct {
	ID        uuid.UUID `json:"id" db:"id"`
	Name      string    `json:"name" db:"name"`
	Query     Query     `json:"query" db:"query"`
	CreatedAt time.Time `json:"createdAt" db:"created_at"`
}

func (m *Module) listQueries(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[namedQuery](r.Context(), tx, `select id, name, query, created_at from epcis_named_query order by name`)
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[namedQuery]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) createQuery(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name  string `json:"name"`
		Query Query  `json:"query"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.Name == "" || strings.ContainsAny(in.Name, "/ ") {
		return httpx.BadRequest("name is required and may not contain spaces or slashes")
	}
	for k := range in.Query {
		if !supported[k] {
			return httpx.BadRequest("unsupported query parameter: " + k)
		}
	}
	if _, _, err := in.Query.sql(); err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		nq, err := db.One[namedQuery](r.Context(), tx, `insert into epcis_named_query (id, tenant_id, name, query) values ($1, app_tenant(), $2, $3)
			returning id, name, query, created_at`, ids.New(), in.Name, in.Query)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "epcis.query.create", Entity: "epcis_named_query", EntityID: nq.ID.String(), After: nq}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, nq)
		return nil
	})
}

func (m *Module) runNamedQuery(w http.ResponseWriter, r *http.Request) error {
	name := chi.URLParam(r, "name")
	var q Query
	err := m.Tx(r.Context(), func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `select query from epcis_named_query where name=$1`, name).Scan(&q)
	})
	if err != nil {
		return err
	}
	for _, k := range []string{"perPage", "nextPageToken"} {
		if v := r.URL.Query().Get(k); v != "" {
			q[k] = v
		}
	}
	return m.writeQuery(w, r, name, q)
}

// --- subscriptions (webhooks) ---

type subscription struct {
	ID             uuid.UUID `json:"id" db:"id"`
	QueryName      *string   `json:"queryName,omitempty" db:"query_name"`
	Query          Query     `json:"query" db:"query"`
	Dest           string    `json:"dest" db:"dest"`
	Active         bool      `json:"active" db:"active"`
	DeliveredCount int64     `json:"deliveredCount" db:"delivered_count"`
	LastError      string    `json:"lastError" db:"last_error"`
	CreatedAt      time.Time `json:"createdAt" db:"created_at"`
	Secret         string    `json:"secret,omitempty" db:"-"`
}

const subCols = `id, query_name, query, dest, active, delivered_count, last_error, created_at`

func (m *Module) listSubs(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[subscription](r.Context(), tx, `select `+subCols+` from epcis_subscription order by created_at desc`)
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[subscription]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) createSub(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		QueryName *string `json:"queryName"`
		Query     Query   `json:"query"`
		Dest      string  `json:"dest"`
		Secret    string  `json:"secret"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	u, err := url.Parse(in.Dest)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return httpx.BadRequest("dest must be an http(s) URL")
	}
	if in.Query == nil {
		in.Query = Query{}
	}
	if _, _, err := in.Query.sql(); err != nil {
		return err
	}
	if in.Secret == "" {
		in.Secret = ids.Secret(24)
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		s, err := db.One[subscription](r.Context(), tx, `insert into epcis_subscription (id, tenant_id, query_name, query, dest, secret) values ($1, app_tenant(), $2,$3,$4,$5)
			returning `+subCols, ids.New(), in.QueryName, in.Query, in.Dest, in.Secret)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "epcis.subscription.create", Entity: "epcis_subscription", EntityID: s.ID.String(), After: s}); err != nil {
			return err
		}
		s.Secret = in.Secret
		httpx.JSON(w, http.StatusCreated, s)
		return nil
	})
}

func (m *Module) deleteSub(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		ct, err := tx.Exec(r.Context(), `update epcis_subscription set active=false where id=$1`, id)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return db.ErrNotFound
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "epcis.subscription.delete", Entity: "epcis_subscription", EntityID: id.String()}); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}

type capturePayload struct {
	CaptureID uuid.UUID `json:"captureId"`
}

type deliverPayload struct {
	CaptureID      uuid.UUID `json:"captureId"`
	SubscriptionID uuid.UUID `json:"subscriptionId"`
}

// notifyJob fans a capture out to one delivery job per active subscription.
func (m *Module) notifyJob(ctx context.Context, j jobs.Job) error {
	var p capturePayload
	if err := json.Unmarshal(j.Payload, &p); err != nil {
		return jobs.Permanent{Err: err}
	}
	return m.DB.Tx(ctx, *j.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id from epcis_subscription where active`)
		if err != nil {
			return err
		}
		var subs []uuid.UUID
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			subs = append(subs, id)
		}
		rows.Close()
		for _, s := range subs {
			if _, err := jobs.Enqueue(ctx, tx, j.TenantID, "epcis.deliver", deliverPayload{CaptureID: p.CaptureID, SubscriptionID: s}, jobs.MaxAttempts(10)); err != nil {
				return err
			}
		}
		return nil
	})
}

// Sign computes the webhook signature header value.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// deliverJob posts the capture's matching events to one subscriber, page by page.
func (m *Module) deliverJob(ctx context.Context, j jobs.Job) error {
	var p deliverPayload
	if err := json.Unmarshal(j.Payload, &p); err != nil {
		return jobs.Permanent{Err: err}
	}
	var s subscription
	var secret string
	var events []lib.Event
	err := m.DB.Tx(ctx, *j.TenantID, func(tx pgx.Tx) error {
		var err error
		s, err = db.One[subscription](ctx, tx, `select `+subCols+` from epcis_subscription where id=$1 and active`, p.SubscriptionID)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select secret from epcis_subscription where id=$1`, p.SubscriptionID).Scan(&secret); err != nil {
			return err
		}
		q := Query{}
		if s.QueryName != nil {
			if err := tx.QueryRow(ctx, `select query from epcis_named_query where name=$1`, *s.QueryName).Scan(&q); err != nil {
				return err
			}
		}
		for k, v := range s.Query {
			q[k] = v
		}
		q["EQ_captureID"] = p.CaptureID.String()
		q["perPage"] = "1000"
		for {
			page, next, err := q.Run(ctx, tx)
			if err != nil {
				return err
			}
			events = append(events, page...)
			if next == "" {
				return nil
			}
			q["nextPageToken"] = next
		}
	})
	if err == db.ErrNotFound {
		return nil // subscription removed
	}
	if err != nil || len(events) == 0 {
		return err
	}
	doc := queryDocument("subscription", events)
	doc["epcisBody"].(map[string]any)["queryResults"].(map[string]any)["subscriptionID"] = s.ID.String()
	body, _ := json.Marshal(doc)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.Dest, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/ld+json")
	req.Header.Set("X-OpenSpoor-Signature", Sign(secret, body))
	req.Header.Set("X-OpenSpoor-Subscription", s.ID.String())
	res, err := m.Client.Do(req)
	status := 0
	if err == nil {
		status = res.StatusCode
		res.Body.Close()
	}
	ok := err == nil && status < 300
	msg := ""
	if !ok {
		msg = fmt.Sprintf("delivery failed: status=%d err=%v", status, err)
	}
	_ = m.DB.Tx(ctx, *j.TenantID, func(tx pgx.Tx) error {
		if ok {
			_, err := tx.Exec(ctx, `update epcis_subscription set delivered_count=delivered_count+$2, last_error='' where id=$1`, s.ID, len(events))
			return err
		}
		_, err := tx.Exec(ctx, `update epcis_subscription set last_error=$2 where id=$1`, s.ID, msg)
		return err
	})
	if !ok {
		return fmt.Errorf("%s", msg)
	}
	return nil
}
