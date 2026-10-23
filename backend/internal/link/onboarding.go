package link

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
	"github.com/openspoor/openspoor/internal/partners"
	lib "github.com/openspoor/openspoor/pkg/epcis"
)

// Onboarding test harness: sends a test message over a partner's configured channel and
// records the result on the partner's onboarding (partners.RecordTest).

// channelOf picks the partner's configured channel.
func channelOf(p partners.Partner) string {
	switch {
	case p.LinkedTenant != nil:
		return "local"
	case p.Endpoints.Federation != "":
		return "federation"
	case strings.HasPrefix(p.Endpoints.EPCISCapture, "http"):
		return "epcis"
	case p.Endpoints.AS2URL != "":
		return "as2"
	}
	return ""
}

func (m *Module) runPartnerTest(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		PartnerID uuid.UUID `json:"partnerId"`
		Channel   string    `json:"channel"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	var p partners.Partner
	if err := m.Tx(ctx, func(tx pgx.Tx) error {
		var err error
		p, err = partners.Get(ctx, tx, in.PartnerID)
		return err
	}); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return httpx.BadRequest("unknown partnerId")
		}
		return err
	}
	ch := in.Channel
	if ch == "" {
		ch = channelOf(p)
	}
	start := time.Now()
	var detail map[string]any
	var err error
	switch ch {
	case "local":
		detail, err = m.testLocal(ctx, p)
	case "federation":
		detail, err = m.testFederation(ctx, p)
	case "epcis":
		detail, err = m.testEPCIS(ctx, p)
	case "as2":
		err = errors.New("AS2 is not a built-in transport; test it through the AS2 gateway")
	case "":
		ch, err = "none", errors.New("the partner has no channel configured")
	default:
		return httpx.BadRequest("channel must be local, federation or epcis")
	}
	if detail == nil {
		detail = map[string]any{}
	}
	detail["latencyMs"] = time.Since(start).Milliseconds()
	if err != nil {
		detail["error"] = err.Error()
	}
	return m.Tx(ctx, func(tx pgx.Tx) error {
		run, rerr := partners.RecordTest(ctx, tx, p.ID, ch, err == nil, detail)
		if rerr != nil {
			return rerr
		}
		o, rerr := partners.LoadOnboarding(ctx, tx, p.ID)
		if rerr != nil {
			return rerr
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"run": run, "onboarding": o})
		return nil
	})
}

// testLocal checks that the partner tenant on this node has an active connection back to us.
func (m *Module) testLocal(ctx context.Context, p partners.Partner) (map[string]any, error) {
	if p.LinkedTenant == nil {
		return nil, errors.New("partner is not on this node")
	}
	me := auth.MustFrom(ctx)
	var status string
	err := m.DB.Tx(ctx, *p.LinkedTenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select status from partner where linked_tenant = $1 order by created_at limit 1`, me.TenantID).Scan(&status)
	})
	detail := map[string]any{"channel": "local", "remoteStatus": status}
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, db.ErrNotFound) {
		return detail, errors.New("the partner has no connection back to this tenant")
	}
	if err != nil {
		return detail, err
	}
	if status != "active" {
		return detail, errors.New("the partner's side of the connection is " + status)
	}
	return detail, nil
}

// testFederation delivers a signed ping to the peer right away (not through the queue).
func (m *Module) testFederation(ctx context.Context, p partners.Partner) (map[string]any, error) {
	peerID, err := uuid.Parse(p.Endpoints.Federation)
	if err != nil {
		return nil, errors.New("partner federation endpoint is not a federation peer id")
	}
	n, err := m.Node(ctx)
	if err != nil {
		return nil, err
	}
	var peer Peer
	var slug string
	err = m.Tx(ctx, func(tx pgx.Tx) error {
		if peer, err = db.One[Peer](ctx, tx, `select `+peerCols+` from federation_peer where id=$1`, peerID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `select slug from tenant where id=app_tenant()`).Scan(&slug)
	})
	if err != nil {
		return nil, err
	}
	detail := map[string]any{"channel": "federation", "peer": peer.Name}
	if peer.Status != "active" {
		return detail, errors.New("federation peer is " + peer.Status)
	}
	env := n.Sign(Envelope{ID: uuid.NewString(), Type: "ping", From: n.DID, FromTenant: slug, To: peer.DID, ToTenant: peer.RemoteTenant,
		CreatedAt: time.Now().UTC(), Payload: json.RawMessage(`{"test":true}`)})
	detail["messageId"] = env.ID
	b, _ := json.Marshal(env)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, peer.URL+"/api/v1/federation/inbox", bytes.NewReader(b))
	if err != nil {
		return detail, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := m.Client.Do(req)
	status := "delivered"
	if err == nil {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
		_ = res.Body.Close()
		detail["status"], detail["response"] = res.StatusCode, string(body)
		if res.StatusCode != http.StatusAccepted {
			status, err = "rejected", errors.New("peer answered HTTP "+res.Status)
		}
	} else {
		status = "rejected"
	}
	_ = m.Tx(ctx, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `insert into federation_message (id, tenant_id, peer_id, direction, msg_id, type, status, detail) values ($1, app_tenant(), $2, 'outbound', $3, 'ping', $4, $5)`,
			ids.New(), peer.ID, env.ID, status, detail)
		return e
	})
	return detail, err
}

// testEPCIS posts an EPCIS 2.0 document with no events to the partner's capture endpoint.
func (m *Module) testEPCIS(ctx context.Context, p partners.Partner) (map[string]any, error) {
	detail := map[string]any{"channel": "epcis", "url": p.Endpoints.EPCISCapture}
	doc, _ := lib.WriteJSON(lib.NewDocument([]lib.Event{}))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Endpoints.EPCISCapture, bytes.NewReader(doc))
	if err != nil {
		return detail, err
	}
	req.Header.Set("Content-Type", "application/ld+json")
	res, err := m.Client.Do(req)
	if err != nil {
		return detail, err
	}
	_ = res.Body.Close()
	detail["status"] = res.StatusCode
	if res.StatusCode >= 300 {
		return detail, errors.New("endpoint answered HTTP " + res.Status)
	}
	return detail, nil
}
