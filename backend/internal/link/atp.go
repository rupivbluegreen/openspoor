package link

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
	"github.com/openspoor/openspoor/internal/partners"
)

// Authorized trading partner (ATP) credentials (ADR-010): W3C Verifiable Credentials Data
// Model 2.0, secured with a Data Integrity proof using the eddsa-jcs-2022 cryptosuite and this
// node's did:web Ed25519 key, with revocation through a W3C Bitstring Status List.

const (
	vcContextV2     = "https://www.w3.org/ns/credentials/v2"
	cryptosuite     = "eddsa-jcs-2022"
	atpType         = "AuthorizedTradingPartnerCredential"
	statusListBits  = 131072 // the specification's minimum list size (16 KiB), for group privacy
	statusPath      = "/api/v1/credentials/status/revocation"
	multikeyVMIndex = "#key-2"
)

// --- Data Integrity: eddsa-jcs-2022 ---

func proofHash(doc, proofConfig map[string]any) ([]byte, error) {
	cfg, err := CanonicalizeValue(proofConfig)
	if err != nil {
		return nil, err
	}
	body, err := CanonicalizeValue(doc)
	if err != nil {
		return nil, err
	}
	a, b := sha256.Sum256(cfg), sha256.Sum256(body)
	return append(a[:], b[:]...), nil
}

// signDataIntegrity adds an eddsa-jcs-2022 DataIntegrityProof to doc (which must not carry one).
func (n *nodeIdentity) signDataIntegrity(doc map[string]any, created time.Time) (map[string]any, error) {
	proof := map[string]any{
		"type":               "DataIntegrityProof",
		"cryptosuite":        cryptosuite,
		"created":            created.UTC().Format(time.RFC3339),
		"verificationMethod": n.DID + multikeyVMIndex,
		"proofPurpose":       "assertionMethod",
	}
	if c, ok := doc["@context"]; ok {
		proof["@context"] = c
	}
	h, err := proofHash(doc, proof)
	if err != nil {
		return nil, err
	}
	proof["proofValue"] = "z" + base58btc(ed25519.Sign(n.priv, h))
	out := make(map[string]any, len(doc)+1)
	for k, v := range doc {
		out[k] = v
	}
	out["proof"] = proof
	return out, nil
}

// verifyDataIntegrity checks an eddsa-jcs-2022 proof with the given key resolver.
func verifyDataIntegrity(secured map[string]any, key func(vm string) (ed25519.PublicKey, error)) (string, error) {
	proof, ok := secured["proof"].(map[string]any)
	if !ok {
		return "", errors.New("credential has no single proof")
	}
	if proof["type"] != "DataIntegrityProof" || proof["cryptosuite"] != cryptosuite {
		return "", fmt.Errorf("unsupported proof: type %v, cryptosuite %v", proof["type"], proof["cryptosuite"])
	}
	if proof["proofPurpose"] != "assertionMethod" {
		return "", errors.New("proofPurpose must be assertionMethod")
	}
	pv, _ := proof["proofValue"].(string)
	if !strings.HasPrefix(pv, "z") {
		return "", errors.New("proofValue must be base58btc multibase")
	}
	sig, err := unbase58btc(pv[1:])
	if err != nil || len(sig) != ed25519.SignatureSize {
		return "", errors.New("proofValue is not an Ed25519 signature")
	}
	vm, _ := proof["verificationMethod"].(string)
	pub, err := key(vm)
	if err != nil {
		return vm, err
	}
	doc := make(map[string]any, len(secured))
	for k, v := range secured {
		if k != "proof" {
			doc[k] = v
		}
	}
	opts := make(map[string]any, len(proof))
	for k, v := range proof {
		if k != "proofValue" {
			opts[k] = v
		}
	}
	if pc, ok := opts["@context"]; ok {
		if !contextPrefix(doc["@context"], pc) {
			return vm, errors.New("proof @context does not match the credential @context")
		}
		doc["@context"] = pc
	}
	if c, ok := doc["@context"]; ok {
		opts["@context"] = c
	}
	h, err := proofHash(doc, opts)
	if err != nil {
		return vm, err
	}
	if !ed25519.Verify(pub, h, sig) {
		return vm, errors.New("signature does not match")
	}
	return vm, nil
}

func contextPrefix(docCtx, proofCtx any) bool {
	toList := func(v any) []any {
		if l, ok := v.([]any); ok {
			return l
		}
		return []any{v}
	}
	d, p := toList(docCtx), toList(proofCtx)
	if len(p) > len(d) {
		return false
	}
	for i := range p {
		a, _ := json.Marshal(d[i])
		b, _ := json.Marshal(p[i])
		if !bytes.Equal(a, b) {
			return false
		}
	}
	return true
}

// --- did:web resolution ---

type didDoc struct {
	ID                 string `json:"id"`
	VerificationMethod []struct {
		ID                 string `json:"id"`
		Type               string `json:"type"`
		Controller         string `json:"controller"`
		PublicKeyMultibase string `json:"publicKeyMultibase"`
		PublicKeyJwk       struct {
			Kty string `json:"kty"`
			Crv string `json:"crv"`
			X   string `json:"x"`
		} `json:"publicKeyJwk"`
	} `json:"verificationMethod"`
	AssertionMethod []any `json:"assertionMethod"`
}

// didWebURL maps did:web:host%3Aport:path to https://host:port/path/did.json (DID Web method).
func (m *Module) didWebURL(did string) (string, error) {
	rest, ok := strings.CutPrefix(did, "did:web:")
	if !ok || rest == "" {
		return "", errors.New("only did:web issuers are supported")
	}
	parts := strings.Split(rest, ":")
	host, err := url.PathUnescape(parts[0])
	if err != nil || host == "" || strings.ContainsAny(host, "/?#@") {
		return "", errors.New("invalid did:web host")
	}
	scheme := "https"
	if m.InsecureDIDWeb {
		scheme = "http"
	}
	path := "/.well-known/did.json"
	if len(parts) > 1 {
		segs := make([]string, len(parts)-1)
		for i, p := range parts[1:] {
			s, err := url.PathUnescape(p)
			if err != nil || s == "" || strings.Contains(s, "/") {
				return "", errors.New("invalid did:web path")
			}
			segs[i] = url.PathEscape(s)
		}
		path = "/" + strings.Join(segs, "/") + "/did.json"
	}
	return scheme + "://" + host + path, nil
}

func (m *Module) getJSON(ctx context.Context, u string, limit int64, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	res, err := m.Client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned HTTP %d", u, res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, limit)).Decode(v)
}

// resolveKey resolves a did:web verification method to an Ed25519 key authorised for assertions.
func (m *Module) resolveKey(ctx context.Context, issuer, vm string) (ed25519.PublicKey, error) {
	if !strings.HasPrefix(vm, issuer+"#") {
		return nil, errors.New("verification method is not controlled by the issuer")
	}
	u, err := m.didWebURL(issuer)
	if err != nil {
		return nil, err
	}
	var doc didDoc
	if err := m.getJSON(ctx, u, 64<<10, &doc); err != nil {
		return nil, fmt.Errorf("cannot resolve %s: %w", issuer, err)
	}
	if doc.ID != issuer {
		return nil, errors.New("DID document id does not match the issuer")
	}
	listed := false
	for _, a := range doc.AssertionMethod {
		if s, ok := a.(string); ok && (s == vm || issuer+s == vm) {
			listed = true
		}
	}
	if !listed {
		return nil, errors.New("verification method is not an assertion method of the issuer")
	}
	for _, v := range doc.VerificationMethod {
		if v.ID != vm && issuer+v.ID != vm {
			continue
		}
		if v.PublicKeyMultibase != "" {
			return parseMultikeyEd25519(v.PublicKeyMultibase)
		}
		if v.PublicKeyJwk.Kty == "OKP" && v.PublicKeyJwk.Crv == "Ed25519" {
			k, err := base64.RawURLEncoding.DecodeString(v.PublicKeyJwk.X)
			if err == nil && len(k) == ed25519.PublicKeySize {
				return k, nil
			}
		}
		return nil, errors.New("verification method has no Ed25519 key")
	}
	return nil, errors.New("verification method not found in the DID document")
}

// --- issuance ---

// Credential is an issued ATP credential.
type Credential struct {
	ID            uuid.UUID      `json:"id" db:"id"`
	LicenseID     uuid.UUID      `json:"licenseId" db:"license_id"`
	StatusIndex   int            `json:"statusIndex" db:"status_index"`
	Credential    map[string]any `json:"credential" db:"credential"`
	ValidFrom     time.Time      `json:"validFrom" db:"valid_from"`
	ValidUntil    *time.Time     `json:"validUntil,omitempty" db:"valid_until"`
	RevokedAt     *time.Time     `json:"revokedAt,omitempty" db:"revoked_at"`
	RevokedReason string         `json:"revokedReason" db:"revoked_reason"`
	CreatedAt     time.Time      `json:"createdAt" db:"created_at"`
}

const atpCols = `id, license_id, status_index, credential, valid_from, valid_until, revoked_at, revoked_reason, created_at`

func (m *Module) statusListURL() string {
	return strings.TrimSuffix(m.Cfg.PublicURL, "/") + statusPath
}

func randomIndex() int {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return int(binary.BigEndian.Uint32(b[:]) % statusListBits)
}

func (m *Module) issueCredential(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		LicenseID uuid.UUID `json:"licenseId"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	n, err := m.Node(ctx)
	if err != nil {
		return err
	}
	return m.Tx(ctx, func(tx pgx.Tx) error {
		// The license register is owned by platform; it is read here inside the tenant's transaction.
		var kind, number, authority string
		var country, orgName, prefix, gln *string
		var from, to *time.Time
		err := tx.QueryRow(ctx, `select l.kind, l.number, l.authority, l.country, l.valid_from, l.valid_to, o.name, o.company_prefix, s.gln
			from license l join organization o on o.id = l.org_id left join site s on s.id = l.site_id where l.id = $1`, in.LicenseID).
			Scan(&kind, &number, &authority, &country, &from, &to, &orgName, &prefix, &gln)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.BadRequest("unknown licenseId")
		}
		if err != nil {
			return err
		}
		now := time.Now().UTC().Truncate(time.Second)
		today := now.Truncate(24 * time.Hour)
		if from != nil && from.After(today) {
			return httpx.Conflict("the license is not valid yet")
		}
		if to != nil && to.Before(today) {
			return httpx.Conflict("the license has expired")
		}
		id := ids.New()
		license := map[string]any{"type": "License", "licenseType": kind, "licenseNumber": number, "issuingAuthority": authority}
		if country != nil && *country != "" {
			license["jurisdiction"] = *country
		}
		if from != nil {
			license["validFrom"] = from.Format("2006-01-02")
		}
		if to != nil {
			license["validUntil"] = to.Format("2006-01-02")
		}
		subject := map[string]any{"type": "Organization", "name": *orgName, "license": license}
		if prefix != nil && *prefix != "" {
			subject["gs1CompanyPrefix"] = *prefix
		}
		if gln != nil {
			subject["gln"] = *gln
		}
		vc := map[string]any{
			"@context":          []any{vcContextV2},
			"id":                "urn:uuid:" + id.String(),
			"type":              []any{"VerifiableCredential", atpType},
			"name":              "Authorized trading partner license credential",
			"issuer":            n.DID,
			"validFrom":         now.Format(time.RFC3339),
			"credentialSubject": subject,
		}
		// TODO(spec): DSCSA defines "authorized" per trading partner type (valid state or federal
		// license or registration). This credential only attests the license recorded here; which
		// license types make a party authorized, and whether a credential must expire earlier than
		// the license, is not settled.
		var until *time.Time
		if to != nil {
			u := to.Add(24*time.Hour - time.Second)
			until = &u
			vc["validUntil"] = u.Format(time.RFC3339)
		}
		var cred Credential
		for attempt := 0; ; attempt++ {
			idx := randomIndex()
			vc["credentialStatus"] = map[string]any{
				"id":                   m.statusListURL() + "#" + strconv.Itoa(idx),
				"type":                 "BitstringStatusListEntry",
				"statusPurpose":        "revocation",
				"statusListIndex":      strconv.Itoa(idx),
				"statusListCredential": m.statusListURL(),
			}
			signed, err := n.signDataIntegrity(vc, now)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `savepoint idx`)
			if err != nil {
				return err
			}
			cred, err = db.One[Credential](ctx, tx, `insert into atp_credential (id, tenant_id, license_id, status_index, credential, valid_from, valid_until)
				values ($1, app_tenant(), $2, $3, $4, $5, $6) returning `+atpCols, id, in.LicenseID, idx, signed, now, until)
			if errors.Is(err, db.ErrConflict) && attempt < 20 {
				if _, err := tx.Exec(ctx, `rollback to savepoint idx`); err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			break
		}
		if _, err := audit.Write(ctx, tx, audit.Record{Action: "atp.credential.issue", Entity: "atp_credential", EntityID: id.String(),
			After: map[string]any{"licenseId": in.LicenseID, "statusIndex": cred.StatusIndex, "validUntil": until}}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, cred)
		return nil
	})
}

func (m *Module) listCredentials(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[Credential](r.Context(), tx, `select `+atpCols+` from atp_credential order by created_at desc limit 500`)
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[Credential]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) getCredential(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		c, err := db.One[Credential](r.Context(), tx, `select `+atpCols+` from atp_credential where id=$1`, id)
		if err == nil {
			httpx.JSON(w, http.StatusOK, c)
		}
		return err
	})
}

func (m *Module) revokeCredential(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if strings.TrimSpace(in.Reason) == "" {
		return httpx.BadRequest("reason is required")
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		c, err := db.One[Credential](r.Context(), tx, `select `+atpCols+` from atp_credential where id=$1`, id)
		if err != nil {
			return err
		}
		if c.RevokedAt != nil {
			return httpx.Conflict("credential is already revoked")
		}
		c, err = db.One[Credential](r.Context(), tx, `update atp_credential set revoked_at=now(), revoked_reason=$2 where id=$1 returning `+atpCols, id, in.Reason)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "atp.credential.revoke", Entity: "atp_credential", EntityID: id.String(),
			After: map[string]any{"reason": in.Reason, "statusIndex": c.StatusIndex}}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, c)
		return nil
	})
}

// statusList serves this node's signed revocation list (public; BitstringStatusListCredential).
func (m *Module) statusList(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	n, err := m.Node(ctx)
	if err != nil {
		return err
	}
	bits := make([]byte, statusListBits/8)
	err = m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select status_index from atp_credential where revoked_at is not null`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var i int
			if err := rows.Scan(&i); err != nil {
				return err
			}
			bits[i/8] |= 0x80 >> (i % 8) // index 0 is the left-most bit
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(bits)
	_ = zw.Close()
	now := time.Now().UTC().Truncate(time.Second)
	list := m.statusListURL()
	vc := map[string]any{
		"@context":  []any{vcContextV2},
		"id":        list,
		"type":      []any{"VerifiableCredential", "BitstringStatusListCredential"},
		"issuer":    n.DID,
		"validFrom": now.Format(time.RFC3339),
		"credentialSubject": map[string]any{
			"id":            list + "#list",
			"type":          "BitstringStatusList",
			"statusPurpose": "revocation",
			"encodedList":   "u" + base64.RawURLEncoding.EncodeToString(gz.Bytes()),
		},
	}
	signed, err := n.signDataIntegrity(vc, now)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "public, max-age=60")
	httpx.JSON(w, http.StatusOK, signed)
	return nil
}

// --- verification ---

// Check is one verification step.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Verification is the stored result of verifying a presented credential.
type Verification struct {
	ID           uuid.UUID      `json:"id" db:"id"`
	PartnerID    *uuid.UUID     `json:"partnerId,omitempty" db:"partner_id"`
	CredentialID string         `json:"credentialId" db:"credential_id"`
	Issuer       string         `json:"issuer" db:"issuer"`
	Subject      string         `json:"subject" db:"subject"`
	Verified     bool           `json:"verified" db:"verified"`
	Checks       []Check        `json:"checks" db:"checks"`
	Credential   map[string]any `json:"credential" db:"credential"`
	CheckedAt    time.Time      `json:"checkedAt" db:"checked_at"`
}

const verCols = `id, partner_id, credential_id, issuer, subject, verified, checks, credential, checked_at`

func issuerOf(vc map[string]any) string {
	switch x := vc["issuer"].(type) {
	case string:
		return x
	case map[string]any:
		s, _ := x["id"].(string)
		return s
	}
	return ""
}

func hasType(v any, t string) bool {
	switch x := v.(type) {
	case string:
		return x == t
	case []any:
		for _, e := range x {
			if e == t {
				return true
			}
		}
	}
	return false
}

func timeField(vc map[string]any, k string) (*time.Time, error) {
	s, ok := vc[k].(string)
	if !ok {
		if _, present := vc[k]; present {
			return nil, fmt.Errorf("%s must be a date-time string", k)
		}
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, fmt.Errorf("%s is not an XML Schema dateTimeStamp", k)
	}
	return &t, nil
}

// VerifyCredential runs every check on a presented credential.
func (m *Module) VerifyCredential(ctx context.Context, vc map[string]any) []Check {
	var checks []Check
	add := func(name string, err error, okDetail string) bool {
		c := Check{Name: name, OK: err == nil, Detail: okDetail}
		if err != nil {
			c.Detail = err.Error()
		}
		checks = append(checks, c)
		return err == nil
	}
	ctxList, _ := vc["@context"].([]any)
	var formatErr error
	switch {
	case len(ctxList) == 0 || ctxList[0] != vcContextV2:
		formatErr = errors.New("@context must start with " + vcContextV2)
	case !hasType(vc["type"], "VerifiableCredential"):
		formatErr = errors.New("type must include VerifiableCredential")
	case vc["credentialSubject"] == nil:
		formatErr = errors.New("credentialSubject is required")
	}
	if !add("format", formatErr, "VC Data Model 2.0") {
		return checks
	}
	var typeErr error
	if !hasType(vc["type"], atpType) {
		typeErr = errors.New("not an " + atpType)
	}
	add("type", typeErr, atpType)
	issuer := issuerOf(vc)
	vm, err := verifyDataIntegrity(vc, func(vm string) (ed25519.PublicKey, error) { return m.resolveKey(ctx, issuer, vm) })
	add("proof", err, "eddsa-jcs-2022 signature by "+vm)
	from, ferr := timeField(vc, "validFrom")
	until, uerr := timeField(vc, "validUntil")
	now := time.Now()
	var vErr error
	switch {
	case ferr != nil:
		vErr = ferr
	case uerr != nil:
		vErr = uerr
	case from != nil && now.Before(*from):
		vErr = errors.New("not valid before " + from.Format(time.RFC3339))
	case until != nil && now.After(*until):
		vErr = errors.New("expired on " + until.Format(time.RFC3339))
	}
	add("validity", vErr, "within validFrom / validUntil")
	add("status", m.checkStatus(ctx, vc, issuer), "not revoked")
	return checks
}

// checkStatus checks a BitstringStatusListEntry for revocation.
func (m *Module) checkStatus(ctx context.Context, vc map[string]any, issuer string) error {
	raw, ok := vc["credentialStatus"]
	if !ok {
		return errors.New("credential has no credentialStatus, so revocation cannot be checked")
	}
	st, ok := raw.(map[string]any)
	if !ok || st["type"] != "BitstringStatusListEntry" || st["statusPurpose"] != "revocation" {
		return errors.New("unsupported credentialStatus (expected a revocation BitstringStatusListEntry)")
	}
	idxS, _ := st["statusListIndex"].(string)
	idx, err := strconv.Atoi(idxS)
	if err != nil || idx < 0 {
		return errors.New("invalid statusListIndex")
	}
	listURL, _ := st["statusListCredential"].(string)
	lu, err := url.Parse(listURL)
	allowed := lu != nil && (lu.Scheme == "https" || m.InsecureDIDWeb && lu.Scheme == "http")
	if err != nil || !allowed {
		return errors.New("statusListCredential must be an https URL")
	}
	didURL, err := m.didWebURL(issuer)
	if err != nil {
		return err
	}
	du, _ := url.Parse(didURL)
	if !strings.EqualFold(lu.Host, du.Host) {
		return errors.New("status list is not hosted by the issuer")
	}
	var list map[string]any
	if err := m.getJSON(ctx, listURL, 1<<20, &list); err != nil {
		return fmt.Errorf("cannot fetch status list: %w", err)
	}
	if issuerOf(list) != issuer || !hasType(list["type"], "BitstringStatusListCredential") {
		return errors.New("status list is not a BitstringStatusListCredential of the issuer")
	}
	if _, err := verifyDataIntegrity(list, func(vm string) (ed25519.PublicKey, error) { return m.resolveKey(ctx, issuer, vm) }); err != nil {
		return fmt.Errorf("status list proof: %w", err)
	}
	if until, err := timeField(list, "validUntil"); err != nil || (until != nil && time.Now().After(*until)) {
		return errors.New("status list has expired")
	}
	subj, _ := list["credentialSubject"].(map[string]any)
	if subj == nil || subj["statusPurpose"] != "revocation" {
		return errors.New("status list purpose is not revocation")
	}
	enc, _ := subj["encodedList"].(string)
	if !strings.HasPrefix(enc, "u") {
		return errors.New("encodedList must be multibase base64url")
	}
	gz, err := base64.RawURLEncoding.DecodeString(enc[1:])
	if err != nil {
		return errors.New("encodedList is not base64url")
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return errors.New("encodedList is not GZIP data")
	}
	bits, err := io.ReadAll(io.LimitReader(zr, 16<<20))
	if err != nil {
		return errors.New("encodedList is not GZIP data")
	}
	if idx >= len(bits)*8 {
		return errors.New("statusListIndex is outside the status list")
	}
	if bits[idx/8]&(0x80>>(idx%8)) != 0 {
		return errors.New("credential has been revoked by its issuer")
	}
	return nil
}

func (m *Module) verifyCredential(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Credential map[string]any `json:"credential"`
		PartnerID  *uuid.UUID     `json:"partnerId"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.Credential == nil {
		return httpx.BadRequest("credential is required")
	}
	ctx := r.Context()
	if in.PartnerID != nil {
		if err := m.Tx(ctx, func(tx pgx.Tx) error {
			_, err := partners.Get(ctx, tx, *in.PartnerID)
			return err
		}); err != nil {
			return httpx.BadRequest("unknown partnerId")
		}
	}
	checks := m.VerifyCredential(ctx, in.Credential)
	ok := true
	for _, c := range checks {
		ok = ok && c.OK
	}
	credID, _ := in.Credential["id"].(string)
	subject := ""
	if s, isMap := in.Credential["credentialSubject"].(map[string]any); isMap {
		subject, _ = s["name"].(string)
	}
	return m.Tx(ctx, func(tx pgx.Tx) error {
		v, err := db.One[Verification](ctx, tx, `insert into atp_verification (id, tenant_id, partner_id, credential_id, issuer, subject, verified, checks, credential)
			values ($1, app_tenant(), $2,$3,$4,$5,$6,$7,$8) returning `+verCols, ids.New(), in.PartnerID, credID, issuerOf(in.Credential), subject, ok, checks, in.Credential)
		if err != nil {
			return err
		}
		if _, err := audit.Write(ctx, tx, audit.Record{Action: "atp.credential.verify", Entity: "atp_verification", EntityID: v.ID.String(),
			After: map[string]any{"verified": ok, "issuer": v.Issuer, "credentialId": credID, "partnerId": in.PartnerID}}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, v)
		return nil
	})
}

func (m *Module) listVerifications(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[Verification](r.Context(), tx, `select `+verCols+` from atp_verification where ($1 = '' or partner_id::text = $1)
			order by checked_at desc limit 200`, r.URL.Query().Get("partnerId"))
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[Verification]{Items: items, Total: len(items)})
		}
		return err
	})
}
