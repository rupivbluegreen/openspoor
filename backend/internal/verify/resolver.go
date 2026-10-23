package verify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
	"github.com/openspoor/openspoor/pkg/gs1"
)

// Audiences a link can be shown to.
const (
	AudConsumer       = "consumer"
	AudAuthority      = "authority"
	AudTradingPartner = "trading_partner"
)

var audiences = []string{AudConsumer, AudAuthority, AudTradingPartner}

// Link types. Identifiers with the gs1: prefix are the GS1 Digital Link link types of the same
// name (IRI https://gs1.org/voc/<name>); the local ones use the openspoor: prefix and are not GS1
// vocabulary.
//
// TODO(spec): confirm the gs1: identifiers and their exact meaning against the current GS1
// Digital Link link type vocabulary (GS1 Web Vocabulary) before relying on them in a
// published specification; replace the local leaflet and consumer verification types if GS1
// publishes equivalents.
const (
	TypePIP                  = "gs1:pip"
	TypeSafetyInfo           = "gs1:safetyInfo"
	TypeRecallStatus         = "gs1:recallStatus"
	TypeLeaflet              = "openspoor:leaflet"
	TypeConsumerVerification = "openspoor:consumerVerification"
	gs1VocPrefix             = "https://gs1.org/voc/"
)

// LinkType describes one link type the resolver knows.
type LinkType struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// IRI is the relation type used in the linkset ("" = the identifier itself).
	IRI string `json:"iri"`
	// Vocabulary is "gs1" or "local".
	Vocabulary string `json:"vocabulary"`
	Note       string `json:"note,omitempty"`
}

var linkTypes = []LinkType{
	{ID: TypePIP, Title: "Product information", IRI: gs1VocPrefix + "pip", Vocabulary: "gs1", Note: "TODO(spec): confirm against the current GS1 link type vocabulary."},
	{ID: TypeSafetyInfo, Title: "Safety information", IRI: gs1VocPrefix + "safetyInfo", Vocabulary: "gs1", Note: "TODO(spec): confirm against the current GS1 link type vocabulary."},
	{ID: TypeRecallStatus, Title: "Recall notice", IRI: gs1VocPrefix + "recallStatus", Vocabulary: "gs1", Note: "TODO(spec): confirm against the current GS1 link type vocabulary."},
	{ID: TypeLeaflet, Title: "Patient information leaflet", IRI: TypeLeaflet, Vocabulary: "local", Note: "Local identifier. TODO(spec): use a GS1 or regulatory identifier once one is fixed."},
	{ID: TypeConsumerVerification, Title: "Consumer verification", IRI: TypeConsumerVerification, Vocabulary: "local", Note: "Local identifier. Generated for a Digital Link that carries a serial number."},
}

var customTypeRE = regexp.MustCompile(`^custom:[a-z][a-z0-9_-]{0,40}$`)

func knownType(id string) (LinkType, bool) {
	for _, t := range linkTypes {
		if t.ID == id || t.IRI == id {
			return t, true
		}
	}
	if customTypeRE.MatchString(id) {
		return LinkType{ID: id, Title: id, IRI: id, Vocabulary: "local"}, true
	}
	return LinkType{}, false
}

// ResolverLink is a stored typed link.
type ResolverLink struct {
	ID        uuid.UUID `json:"id" db:"id"`
	GTIN      string    `json:"gtin" db:"gtin"`
	Lot       string    `json:"lot" db:"lot"`
	LinkType  string    `json:"linkType" db:"link_type"`
	Audiences []string  `json:"audiences" db:"audiences"`
	MIMEType  string    `json:"mimeType" db:"mime_type"`
	Lang      string    `json:"lang" db:"lang"`
	Href      string    `json:"href" db:"href"`
	Title     string    `json:"title" db:"title"`
	IsDefault bool      `json:"isDefault" db:"is_default"`
	Active    bool      `json:"active" db:"active"`
	CreatedBy string    `json:"createdBy" db:"created_by"`
	CreatedAt time.Time `json:"createdAt" db:"created_at"`
	UpdatedAt time.Time `json:"updatedAt" db:"updated_at"`
}

const linkCols = `id, gtin, lot, link_type, audiences, mime_type, lang, href, title, is_default, active, created_by, created_at, updated_at`

// Resolved is one link in a resolution.
type Resolved struct {
	LinkType  string   `json:"linkType"`
	Href      string   `json:"href"`
	Type      string   `json:"type,omitempty"`
	Lang      string   `json:"lang,omitempty"`
	Title     string   `json:"title,omitempty"`
	Audiences []string `json:"audiences"`
	Default   bool     `json:"default,omitempty"`
	// Source is "stored" (a link the brand owner published) or "generated" (derived from the
	// verification service or the consumer information).
	Source string `json:"source"`
}

func hasAudience(l Resolved, aud string) bool {
	for _, a := range l.Audiences {
		if a == aud || a == AudConsumer {
			return true
		}
	}
	return false
}

// relation returns the relation type (an IRI) of a link type id.
func relation(id string) string {
	if t, ok := knownType(id); ok && t.IRI != "" {
		return t.IRI
	}
	return id
}

// --- content negotiation ---

type mediaRange struct {
	value string
	q     float64
}

// parseAccept parses an Accept or Accept-Language header into ranges, highest q first.
func parseAccept(h string) []mediaRange {
	var out []mediaRange
	for _, part := range strings.Split(h, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		v := strings.ToLower(strings.TrimSpace(fields[0]))
		if v == "" {
			continue
		}
		q := 1.0
		for _, p := range fields[1:] {
			if k, val, ok := strings.Cut(strings.TrimSpace(p), "="); ok && strings.TrimSpace(k) == "q" {
				if f, err := strconv.ParseFloat(strings.TrimSpace(val), 64); err == nil && f >= 0 && f <= 1 {
					q = f
				}
			}
		}
		out = append(out, mediaRange{v, q})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].q > out[j].q })
	return out
}

// mimeScore: 0 = not acceptable, otherwise the q of the most specific matching range.
func mimeScore(ranges []mediaRange, mime string) float64 {
	if len(ranges) == 0 || mime == "" {
		return 1
	}
	mime = strings.ToLower(mime)
	best, spec := 0.0, -1
	for _, r := range ranges {
		s := -1
		switch {
		case r.value == mime:
			s = 2
		case strings.HasSuffix(r.value, "/*") && strings.HasPrefix(mime, strings.TrimSuffix(r.value, "*")):
			s = 1
		case r.value == "*/*":
			s = 0
		}
		if s > spec {
			spec, best = s, r.q
		}
	}
	return best
}

// langScore: 0 = not acceptable. A link without a language is acceptable at a low rank.
func langScore(ranges []mediaRange, lang string) float64 {
	if len(ranges) == 0 {
		return 1
	}
	if lang == "" {
		return 0.001
	}
	lang = strings.ToLower(lang)
	prim, _, _ := strings.Cut(lang, "-")
	best := 0.0
	for _, r := range ranges {
		rp, _, _ := strings.Cut(r.value, "-")
		switch {
		case r.value == "*":
			best = max(best, r.q*0.01)
		case r.value == lang:
			best = max(best, r.q)
		case rp == prim:
			best = max(best, r.q*0.9)
		}
	}
	return best
}

// Negotiate picks the best link for the Accept and Accept-Language headers: media type first,
// then language, then the brand owner's default flag, then the order given.
func Negotiate(cands []Resolved, accept, acceptLang string) (Resolved, bool) {
	am, al := parseAccept(accept), parseAccept(acceptLang)
	type scored struct {
		l       Resolved
		m, g    float64
		def, ix int
	}
	var best *scored
	for i, c := range cands {
		m, g := mimeScore(am, c.Type), langScore(al, c.Lang)
		if m == 0 || g == 0 {
			continue
		}
		d := 0
		if c.Default {
			d = 1
		}
		s := scored{c, m, g, d, i}
		if best == nil || s.m > best.m || (s.m == best.m && (s.g > best.g || (s.g == best.g && s.def > best.def))) {
			best = &s
		}
	}
	if best == nil {
		return Resolved{}, false
	}
	return best.l, true
}

// Linkset builds the RFC 9264 linkset (application/linkset+json) for an anchor.
func Linkset(anchor string, links []Resolved) map[string]any {
	ctx := map[string]any{"anchor": anchor}
	for _, l := range links {
		t := map[string]any{"href": l.Href}
		if l.Type != "" {
			t["type"] = l.Type
		}
		if l.Lang != "" {
			t["hreflang"] = []string{l.Lang}
		}
		if l.Title != "" {
			t["title"] = l.Title
		}
		rel := relation(l.LinkType)
		list, _ := ctx[rel].([]map[string]any)
		ctx[rel] = append(list, t)
	}
	return map[string]any{"linkset": []map[string]any{ctx}}
}

// linkHeader renders RFC 8288 Link header values (at most max).
func linkHeader(links []Resolved, linksetURL string, max int) []string {
	out := []string{fmt.Sprintf(`<%s>; rel="linkset"; type="application/linkset+json"`, linksetURL)}
	for i, l := range links {
		if i >= max {
			break
		}
		if strings.ContainsAny(l.Href, "<>\"\r\n") {
			continue
		}
		v := fmt.Sprintf(`<%s>; rel="%s"`, l.Href, relation(l.LinkType))
		if l.Type != "" {
			v += `; type="` + l.Type + `"`
		}
		if l.Lang != "" {
			v += `; hreflang="` + l.Lang + `"`
		}
		if t := strings.NewReplacer(`"`, `'`, "\r", " ", "\n", " ").Replace(l.Title); t != "" {
			v += `; title="` + t + `"`
		}
		out = append(out, v)
	}
	return out
}

// --- stored links: validation and management ---

var (
	mimeRE = regexp.MustCompile(`^[a-z]+/[a-z0-9.+-]+$`)
	langRE = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)
)

// linkInput is the body of create and update.
type linkInput struct {
	GTIN      string   `json:"gtin"`
	Lot       string   `json:"lot"`
	LinkType  string   `json:"linkType"`
	Audiences []string `json:"audiences"`
	MIMEType  string   `json:"mimeType"`
	Lang      string   `json:"lang"`
	Href      string   `json:"href"`
	Title     string   `json:"title"`
	IsDefault bool     `json:"isDefault"`
	Active    *bool    `json:"active"`
}

func (in *linkInput) validate() error {
	in.GTIN = gs1.NormalizeGTIN(strings.TrimSpace(in.GTIN))
	if !gs1.ValidGTIN(in.GTIN) {
		return httpx.BadRequest("gtin must be a valid GTIN")
	}
	if len(in.Lot) > 20 {
		return httpx.BadRequest("lot is at most 20 characters")
	}
	t, ok := knownType(in.LinkType)
	if !ok {
		return httpx.BadRequest("linkType must be one of " + strings.Join(typeIDs(), ", ") + ", or custom:<name>")
	}
	in.LinkType = t.ID
	if len(in.Audiences) == 0 {
		in.Audiences = []string{AudConsumer}
	}
	seen := map[string]bool{}
	var auds []string
	for _, a := range in.Audiences {
		if !contains(audiences, a) {
			return httpx.BadRequest("audiences must be from " + strings.Join(audiences, ", "))
		}
		if !seen[a] {
			seen[a] = true
			auds = append(auds, a)
		}
	}
	in.Audiences = auds
	if in.MIMEType == "" {
		in.MIMEType = "text/html"
	}
	if !mimeRE.MatchString(in.MIMEType) {
		return httpx.BadRequest("mimeType must look like type/subtype")
	}
	if in.Lang != "" && !langRE.MatchString(in.Lang) {
		return httpx.BadRequest("lang must be a language tag such as en or pt-BR")
	}
	u, err := url.Parse(in.Href)
	if err != nil || u.Scheme != "https" || u.Host == "" || len(in.Href) > 2000 || strings.ContainsAny(in.Href, " <>\"\r\n") {
		return httpx.BadRequest("href must be an https:// address without spaces, at most 2000 characters")
	}
	if len(in.Title) > 200 {
		return httpx.BadRequest("title is at most 200 characters")
	}
	return nil
}

func typeIDs() []string {
	out := make([]string, len(linkTypes))
	for i, t := range linkTypes {
		out[i] = t.ID
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func (m *Module) resolverManageRoutes(r chi.Router) {
	r.Get("/resolver/link-types", httpx.Handler(m.listLinkTypes))
	r.Get("/resolver/links", httpx.Handler(m.listLinks))
	w := r.With(auth.Require(auth.RoleAdmin, auth.RoleCompliance))
	w.Post("/resolver/links", httpx.Handler(m.createLink))
	w.Put("/resolver/links/{id}", httpx.Handler(m.updateLink))
	w.Delete("/resolver/links/{id}", httpx.Handler(m.deleteLink))
}

func (m *Module) listLinkTypes(w http.ResponseWriter, _ *http.Request) error {
	httpx.JSON(w, http.StatusOK, map[string]any{"items": linkTypes, "audiences": audiences, "custom": "custom:<name>"})
	return nil
}

func (m *Module) listLinks(w http.ResponseWriter, r *http.Request) error {
	gtin := r.URL.Query().Get("gtin")
	return m.list(w, r, func(tx pgx.Tx) (any, error) {
		return db.List[ResolverLink](r.Context(), tx, `select `+linkCols+` from resolver_link where tenant_id = app_tenant() and ($1='' or gtin=$1) order by gtin, lot, link_type, lang limit 1000`, gtin)
	})
}

func (m *Module) createLink(w http.ResponseWriter, r *http.Request) error {
	var in linkInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := in.validate(); err != nil {
		return err
	}
	active := in.Active == nil || *in.Active
	p := auth.MustFrom(r.Context())
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		l, err := db.One[ResolverLink](r.Context(), tx, `insert into resolver_link (id, tenant_id, gtin, lot, link_type, audiences, mime_type, lang, href, title, is_default, active, created_by)
			values ($1, app_tenant(), $2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) returning `+linkCols,
			ids.New(), in.GTIN, in.Lot, in.LinkType, in.Audiences, in.MIMEType, in.Lang, in.Href, in.Title, in.IsDefault, active, p.AuditActor())
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "resolver.link.create", Entity: "resolver_link", EntityID: l.ID.String(), After: l}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, l)
		return nil
	})
}

func (m *Module) updateLink(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in linkInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := in.validate(); err != nil {
		return err
	}
	active := in.Active == nil || *in.Active
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		before, err := db.One[ResolverLink](r.Context(), tx, `select `+linkCols+` from resolver_link where tenant_id = app_tenant() and id=$1 for update`, id)
		if err != nil {
			return err
		}
		l, err := db.One[ResolverLink](r.Context(), tx, `update resolver_link set gtin=$2, lot=$3, link_type=$4, audiences=$5, mime_type=$6, lang=$7, href=$8, title=$9, is_default=$10, active=$11, updated_at=now()
			where tenant_id = app_tenant() and id=$1 returning `+linkCols, id, in.GTIN, in.Lot, in.LinkType, in.Audiences, in.MIMEType, in.Lang, in.Href, in.Title, in.IsDefault, active)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "resolver.link.update", Entity: "resolver_link", EntityID: id.String(), Before: before, After: l}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, l)
		return nil
	})
}

func (m *Module) deleteLink(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		before, err := db.One[ResolverLink](r.Context(), tx, `delete from resolver_link where tenant_id = app_tenant() and id=$1 returning `+linkCols, id)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "resolver.link.delete", Entity: "resolver_link", EntityID: id.String(), Before: before}); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}

// --- the resolver ---

// Root mounts the Digital Link resolver at /01/... (kernel.RootRoutes).
func (m *Module) Root(r chi.Router) {
	r.With(m.limit).Get("/01/*", httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		return m.resolve(w, r, "01/"+chi.URLParam(r, "*"))
	}))
}

func (m *Module) publicResolver(r chi.Router) {
	r.Get("/resolve/*", httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		return m.resolve(w, r, chi.URLParam(r, "*"))
	}))
}

// Key holds the identifiers in a resolved Digital Link.
type linkKey struct{ GTIN, Lot, Serial string }

func parseKey(path string) (linkKey, error) {
	ais, err := gs1.ParseDigitalLink("https://resolver.invalid/" + strings.TrimPrefix(path, "/"))
	if err != nil {
		return linkKey{}, httpx.BadRequest("not a GS1 Digital Link: " + err.Error())
	}
	var k linkKey
	for _, a := range ais {
		switch a.Code {
		case "01":
			k.GTIN = a.Value
		case "10":
			k.Lot = a.Value
		case "21":
			k.Serial = a.Value
		case "00", "414", "8004":
			return k, httpx.NotFound("this resolver serves GTIN (01) links only")
		}
	}
	if !gs1.ValidGTIN(k.GTIN) {
		return k, httpx.BadRequest("the GTIN in the link is not valid")
	}
	return k, nil
}

func (k linkKey) path() string {
	p := "/01/" + k.GTIN
	if k.Lot != "" {
		p += "/10/" + url.PathEscape(k.Lot)
	}
	if k.Serial != "" {
		p += "/21/" + url.PathEscape(k.Serial)
	}
	return p
}

// audienceFor decides the audience of a request. Anonymous callers are consumers. Another
// audience needs credentials: trading_partner for users of the brand owner or of an active linked
// partner tenant; authority only for the brand owner's own Admin or Compliance users.
// TODO(spec): how a competent authority identifies itself to a resolver is not defined here;
// authority access is granted by the brand owner issuing credentials (for example an API key).
func (m *Module) audienceFor(ctx context.Context, r *http.Request, owner uuid.UUID) (string, error) {
	want := r.URL.Query().Get("audience")
	if want == "" {
		want = AudConsumer
	}
	if !contains(audiences, want) {
		return "", httpx.BadRequest("audience must be one of " + strings.Join(audiences, ", "))
	}
	if want == AudConsumer {
		return want, nil
	}
	p, err := m.Auth.Authenticate(r)
	if err != nil {
		return "", httpx.Unauthorized("the " + want + " audience needs credentials")
	}
	switch {
	case p.TenantID == owner && (want == AudTradingPartner || p.Has(auth.RoleCompliance)):
		return want, nil
	case want == AudTradingPartner && p.TenantID != owner:
		var ok bool
		err := m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `select exists(select 1 from partner where tenant_id=$1 and linked_tenant=$2 and status='active')`, owner, p.TenantID).Scan(&ok)
		})
		if err != nil {
			return "", err
		}
		if ok {
			return want, nil
		}
	}
	return "", httpx.Forbidden("these credentials may not read the " + want + " links of this product")
}

// loadLinks returns the stored and generated links for a key, before audience filtering.
func (m *Module) loadLinks(ctx context.Context, owner uuid.UUID, k linkKey) (links []Resolved, recalled bool, err error) {
	base := strings.TrimSuffix(m.Cfg.PublicURL, "/")
	err = m.DB.Tx(ctx, owner, func(tx pgx.Tx) error {
		lot := k.Lot
		if lot == "" && k.Serial != "" {
			_ = tx.QueryRow(ctx, `select coalesce(lot,'') from serial_state where tenant_id = app_tenant() and epc=$1`,
				gs1.DigitalLink(gs1.DefaultResolver, gs1.Pack{GTIN: k.GTIN, Serial: k.Serial})).Scan(&lot)
		}
		stored, err := db.List[ResolverLink](ctx, tx, `select `+linkCols+` from resolver_link where tenant_id = app_tenant() and gtin=$1 and active and (lot='' or lot=$2)
			order by is_default desc, updated_at desc`, k.GTIN, lot)
		if err != nil {
			return err
		}
		for _, l := range stored {
			links = append(links, Resolved{LinkType: l.LinkType, Href: l.Href, Type: l.MIMEType, Lang: l.Lang, Title: l.Title, Audiences: l.Audiences, Default: l.IsDefault, Source: "stored"})
		}
		infos, err := db.List[ConsumerInfo](ctx, tx, `select `+infoCols+` from consumer_info where tenant_id = app_tenant() and gtin=$1 and leaflet_url <> '' order by lang`, k.GTIN)
		if err != nil {
			return err
		}
		for _, ci := range infos {
			links = append(links, Resolved{LinkType: TypeLeaflet, Href: ci.LeafletURL, Lang: ci.Lang, Title: "Patient information leaflet", Audiences: []string{AudConsumer}, Source: "generated"})
		}
		if lot != "" {
			if err := tx.QueryRow(ctx, `select exists(select 1 from lot_recall where tenant_id = app_tenant() and gtin=$1 and lot=$2 and status='open')`, k.GTIN, lot).Scan(&recalled); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if k.Serial != "" {
		code := gs1.DigitalLink(gs1.DefaultResolver, gs1.Pack{GTIN: k.GTIN, Lot: k.Lot, Serial: k.Serial})
		verifyURL := base + "/verify/?code=" + url.QueryEscape(code)
		links = append(links, Resolved{LinkType: TypeConsumerVerification, Href: verifyURL, Type: "text/html", Title: "Verify this pack", Audiences: []string{AudConsumer}, Source: "generated"})
		if recalled && !hasType(links, TypeRecallStatus) {
			links = append(links, Resolved{LinkType: TypeRecallStatus, Href: verifyURL, Type: "text/html", Title: "Recall status of this pack", Audiences: []string{AudConsumer}, Source: "generated"})
		}
	}
	return links, recalled, nil
}

func hasType(links []Resolved, t string) bool {
	for _, l := range links {
		if l.LinkType == t {
			return true
		}
	}
	return false
}

func (m *Module) resolve(w http.ResponseWriter, r *http.Request, path string) error {
	ctx := r.Context()
	k, err := parseKey(path)
	if err != nil {
		return err
	}
	owner, err := m.owner(ctx, k.GTIN)
	if err != nil {
		return err
	}
	aud, err := m.audienceFor(ctx, r, owner)
	if err != nil {
		return err
	}
	all, recalled, err := m.loadLinks(ctx, owner, k)
	if err != nil {
		return err
	}
	var visible []Resolved
	for _, l := range all {
		if hasAudience(l, aud) {
			visible = append(visible, l)
		}
	}
	anchor := strings.TrimSuffix(m.Cfg.PublicURL, "/") + k.path()
	linksetURL := anchor + "?linkType=all"
	if aud != AudConsumer {
		linksetURL += "&audience=" + aud
	}
	h := w.Header()
	h.Set("Vary", "Accept, Accept-Language, Authorization, X-API-Key")
	if aud == AudConsumer {
		h.Set("Cache-Control", "public, max-age=60")
	} else {
		h.Set("Cache-Control", "private, no-store")
	}
	for _, v := range linkHeader(visible, linksetURL, 20) {
		h.Add("Link", v)
	}
	accept := r.Header.Get("Accept")
	lt := r.URL.Query().Get("linkType")
	switch {
	case lt == "all" || (lt == "" && wantsMedia(accept, "application/linkset+json")):
		h.Set("Content-Type", "application/linkset+json")
		w.WriteHeader(http.StatusOK)
		return json.NewEncoder(w).Encode(Linkset(anchor, visible))
	case lt == "" && wantsMedia(accept, "application/json"):
		httpx.JSON(w, http.StatusOK, map[string]any{"anchor": anchor, "audience": aud, "recalled": recalled, "links": visible})
		return nil
	}
	var cands []Resolved
	if lt != "" {
		t, ok := knownType(lt)
		if !ok {
			return httpx.BadRequest("unknown linkType " + lt)
		}
		for _, l := range visible {
			if l.LinkType == t.ID {
				cands = append(cands, l)
			}
		}
	} else {
		cands = defaultCandidates(visible)
	}
	best, ok := Negotiate(cands, redirectAccept(accept), r.Header.Get("Accept-Language"))
	if !ok {
		best, ok = Negotiate(cands, "", "") // nothing matches the headers: the first candidate is still better than none
	}
	if !ok {
		return httpx.NotFound("no link of this type is published for this product")
	}
	h.Set("Location", best.Href)
	h.Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusTemporaryRedirect)
	_, _ = fmt.Fprintf(w, "Redirecting to %s\n", best.Href)
	return nil
}

// defaultCandidates is the pool for a request without linkType: links marked default, else the
// consumer verification link (when the link names a serial), else product information, else all.
func defaultCandidates(links []Resolved) []Resolved {
	var d, v, p []Resolved
	for _, l := range links {
		switch {
		case l.Default:
			d = append(d, l)
		case l.LinkType == TypeConsumerVerification:
			v = append(v, l)
		case l.LinkType == TypePIP:
			p = append(p, l)
		}
	}
	for _, pool := range [][]Resolved{d, v, p, links} {
		if len(pool) > 0 {
			return pool
		}
	}
	return nil
}

// wantsMedia reports whether the Accept header lists mt explicitly.
func wantsMedia(accept, mt string) bool {
	for _, r := range parseAccept(accept) {
		if r.value == mt && r.q > 0 {
			return true
		}
	}
	return false
}

// redirectAccept drops the JSON media types a client lists for the descriptive responses, so a
// redirect is negotiated on the document types of the links.
func redirectAccept(accept string) string {
	var keep []string
	for _, r := range parseAccept(accept) {
		if r.value != "application/linkset+json" && r.value != "application/json" {
			keep = append(keep, r.value+";q="+strconv.FormatFloat(r.q, 'f', 3, 64))
		}
	}
	return strings.Join(keep, ",")
}
