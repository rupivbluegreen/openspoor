package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"

	"github.com/google/uuid"
)

// Pseudonymisation keeps the append-only chain compatible with GDPR erasure. Audit rows cannot
// be edited, so personal data is replaced before it is written: actors are user ids (resolvable
// while the tenant exists), and e-mail addresses, phone numbers and person names in snapshots
// become keyed hashes. The key is per tenant and is destroyed with the tenant's data key, after
// which the tokens cannot be linked back to a person while the chain still verifies.
// Free-text fields that people type (a case note, an assistant question) are not scrubbed.

// Pseudonymizer tokenises one personal value for a tenant.
type Pseudonymizer func(ctx context.Context, tenant uuid.UUID, value string) (string, error)

var pseudonymizer atomic.Pointer[Pseudonymizer]

// SetPseudonymizer installs the tokeniser; one installation (process) has one keyring.
func SetPseudonymizer(p Pseudonymizer) { pseudonymizer.Store(&p) }

// Token marks a pseudonymised value.
const Token = "pseudo:"

// personalKeys are snapshot keys (lower case) whose string values identify a person, in any entity.
var personalKeys = map[string]bool{"email": true, "contactemail": true, "invitedemail": true, "signer": true,
	"phone": true, "telephone": true, "mobile": true, "recipient": true}

// personalNames are person-name keys, only for entities that hold people.
var personalNames = map[string]map[string]bool{
	"user":               {"name": true, "displayname": true},
	"partner_invitation": {"name": true},
}

func (r Record) hasPersonalData(b []byte) bool {
	low := bytes.ToLower(b)
	for k := range personalKeys {
		if bytes.Contains(low, []byte(`"`+k+`"`)) {
			return true
		}
	}
	for k := range personalNames[r.Entity] {
		if bytes.Contains(low, []byte(`"`+k+`"`)) {
			return true
		}
	}
	return false
}

// scrub replaces personal values in a JSON snapshot. It calls the pseudonymizer only when a
// personal value is present, so writes without personal data never touch the keyring.
func scrub(ctx context.Context, tenant uuid.UUID, rec Record, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || !rec.hasPersonalData(raw) {
		return raw, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw, nil
	}
	changed := false
	var walk func(any) (any, error)
	walk = func(n any) (any, error) {
		switch x := n.(type) {
		case map[string]any:
			for k, val := range x {
				lk := strings.ToLower(k)
				if s, ok := val.(string); ok && s != "" && !strings.HasPrefix(s, Token) && (personalKeys[lk] || personalNames[rec.Entity][lk]) {
					t, err := tokenise(ctx, tenant, s)
					if err != nil {
						return nil, err
					}
					x[k], changed = t, true
					continue
				}
				nv, err := walk(val)
				if err != nil {
					return nil, err
				}
				x[k] = nv
			}
		case []any:
			for i, val := range x {
				nv, err := walk(val)
				if err != nil {
					return nil, err
				}
				x[i] = nv
			}
		}
		return n, nil
	}
	if _, err := walk(v); err != nil || !changed {
		return raw, err
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return canon(out), nil
}

func tokenise(ctx context.Context, tenant uuid.UUID, value string) (string, error) {
	p := pseudonymizer.Load()
	if p == nil {
		return Token + "unavailable", nil // never write the clear value because no keyring is wired
	}
	t, err := (*p)(ctx, tenant, value)
	if err != nil {
		return "", err
	}
	return Token + t, nil
}

// scrubActor turns "user:alice@example.com" into "user:pseudo:..." as a safety net; callers
// should pass ids (Principal.AuditActor), which need no tokenising.
func scrubActor(ctx context.Context, tenant uuid.UUID, actor string) (string, error) {
	i := strings.IndexByte(actor, ':')
	if i < 0 || !strings.Contains(actor[i+1:], "@") {
		return actor, nil
	}
	t, err := tokenise(ctx, tenant, actor[i+1:])
	if err != nil {
		return "", err
	}
	return actor[:i+1] + t, nil
}
