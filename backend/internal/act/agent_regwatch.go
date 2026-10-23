package act

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
)

// reg-watch is a deterministic watcher over a tenant's configured regulator pages and
// specifications (table watch_source). It fetches each source (https only, size and time limited,
// never a non-public address), reduces it to normalized text, and compares that text's hash with
// the last one. The first fetch only records a baseline. On a change it proposes a case
// "Regulatory source changed: <name>" with a unified diff excerpt and the tenant's products whose
// target markets include the source's market. No language model reads the page. What the change
// means for the company is decided by a person.
const (
	watchMinInterval = 6 * time.Hour // sources are fetched at most this often
	watchPerRun      = 20
	maxLastText      = 64 << 10
	maxSources       = 50
	diffContext      = 3
	diffMaxLines     = 60
)

type regWatch struct{ m *Module }

func (regWatch) polished()    {}
func (regWatch) Name() string { return "reg-watch" }
func (regWatch) Description() string {
	return "Fetches the regulator pages and specifications you watch, compares them with the last version and, on a change, opens an impact case with a diff excerpt and the products in the affected market."
}

var (
	reScript   = regexp.MustCompile(`(?is)<(script|style|noscript|template)\b.*?</(script|style|noscript|template)\s*>`)
	reComment  = regexp.MustCompile(`(?s)<!--.*?-->`)
	reBlockEnd = regexp.MustCompile(`(?i)</(p|div|li|ul|ol|h[1-6]|tr|table|section|article|header|footer|main|pre|blockquote)\s*>|<br\s*/?>|<hr\s*/?>`)
	reTag      = regexp.MustCompile(`(?s)<[^>]*>`)
	reSpaces   = regexp.MustCompile(`[ \t\f\v\x{a0}]+`)
	reToken    = regexp.MustCompile(`(?s)<(/?)([a-zA-Z][a-zA-Z0-9]*)([^>]*)>`)
	reID       = regexp.MustCompile(`(?i)\bid\s*=\s*["']([^"']*)["']`)
	reClass    = regexp.MustCompile(`(?i)\bclass\s*=\s*["']([^"']*)["']`)
	reSelector = regexp.MustCompile(`^([a-zA-Z][a-zA-Z0-9]*)?([#.][A-Za-z0-9_-]+)?$`)
)

// ValidSelector reports whether s is a supported selector: empty, a tag, #id, .class, tag#id or tag.class.
func ValidSelector(s string) bool { return s == "" || reSelector.MatchString(s) }

// extractSelector returns the inner HTML of the first element matching the selector.
func extractSelector(doc, sel string) (string, error) {
	m := reSelector.FindStringSubmatch(sel)
	if m == nil {
		return "", fmt.Errorf("unsupported selector %q", sel)
	}
	tag, idc := strings.ToLower(m[1]), m[2]
	toks := reToken.FindAllStringSubmatchIndex(doc, -1)
	for i, t := range toks {
		if doc[t[2]:t[3]] == "/" {
			continue
		}
		name := strings.ToLower(doc[t[4]:t[5]])
		attrs := doc[t[6]:t[7]]
		if (tag != "" && name != tag) || strings.HasSuffix(attrs, "/") {
			continue
		}
		if idc != "" {
			want := idc[1:]
			if idc[0] == '#' {
				if a := reID.FindStringSubmatch(attrs); a == nil || a[1] != want {
					continue
				}
			} else if a := reClass.FindStringSubmatch(attrs); a == nil || !containsWord(a[1], want) {
				continue
			}
		}
		depth := 1
		for _, u := range toks[i+1:] {
			if strings.ToLower(doc[u[4]:u[5]]) != name {
				continue
			}
			if doc[u[2]:u[3]] == "/" {
				depth--
			} else if !strings.HasSuffix(doc[u[6]:u[7]], "/") {
				depth++
			}
			if depth == 0 {
				return doc[t[1]:u[0]], nil
			}
		}
		return doc[t[1]:], nil // unclosed element: take the rest
	}
	return "", fmt.Errorf("selector %q matched nothing", sel)
}

func containsWord(list, w string) bool {
	for _, f := range strings.Fields(list) {
		if f == w {
			return true
		}
	}
	return false
}

// normalizeText reduces a fetched document to comparable text: markup, scripts and comments are
// removed, entities decoded, whitespace collapsed, and one line per block.
func normalizeText(body []byte, mediaType, selector string) (string, error) {
	text := strings.ToValidUTF8(string(body), "")
	switch {
	case strings.Contains(mediaType, "html") || mediaType == "":
		if selector != "" {
			var err error
			if text, err = extractSelector(text, selector); err != nil {
				return "", err
			}
		}
		text = reComment.ReplaceAllString(text, "")
		text = reScript.ReplaceAllString(text, "")
		text = reBlockEnd.ReplaceAllString(text, "\n")
		text = reTag.ReplaceAllString(text, " ")
		text = html.UnescapeString(text)
	case strings.HasPrefix(mediaType, "text/") || strings.Contains(mediaType, "json") || strings.Contains(mediaType, "xml"):
	default: // PDF and other binary documents: only a content fingerprint can be compared
		sum := sha256.Sum256(body)
		return fmt.Sprintf("binary document (%s), %d bytes, sha256 %s", mediaType, len(body), hex.EncodeToString(sum[:])), nil
	}
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
		if l = strings.TrimSpace(reSpaces.ReplaceAllString(l, " ")); l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n"), nil
}

func hashText(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// unifiedDiff returns a one-hunk unified diff of two texts (common head and tail removed, a few
// lines of context kept, long changes cut) or "" when they are equal.
func unifiedDiff(oldText, newText string) string {
	a, b := strings.Split(oldText, "\n"), strings.Split(newText, "\n")
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	if pre == len(a) && pre == len(b) {
		return ""
	}
	start := max(0, pre-diffContext)
	endA, endB := min(len(a), len(a)-suf+diffContext), min(len(b), len(b)-suf+diffContext)
	var sb strings.Builder
	sb.WriteString("--- previous\n+++ current\n")
	fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", start+1, endA-start, start+1, endB-start)
	for _, l := range a[start:pre] {
		sb.WriteString(" " + l + "\n")
	}
	emit := func(prefix string, ls []string) {
		for i, l := range ls {
			if i == diffMaxLines {
				fmt.Fprintf(&sb, "%s... (%d more lines)\n", prefix, len(ls)-diffMaxLines)
				return
			}
			sb.WriteString(prefix + clip(l, 300) + "\n")
		}
	}
	emit("-", a[pre:len(a)-suf])
	emit("+", b[pre:len(b)-suf])
	for _, l := range a[len(a)-suf : endA] {
		sb.WriteString(" " + l + "\n")
	}
	return sb.String()
}

type watchRow struct {
	id                    uuid.UUID
	name, url, sel, mkt   string
	lastHash, lastText    string
	lastChecked, lastChgd *time.Time
}

func (a regWatch) Observe(ctx context.Context, tx pgx.Tx) ([]Proposal, error) {
	rows, err := tx.Query(ctx, `select id, name, url, selector, market, last_hash, last_text, last_checked_at, last_changed_at from watch_source
		where enabled and (last_checked_at is null or last_checked_at < now() - make_interval(secs => $1)) order by last_checked_at nulls first limit $2`,
		watchMinInterval.Seconds(), watchPerRun)
	if err != nil {
		return nil, err
	}
	var srcs []watchRow
	for rows.Next() {
		var w watchRow
		if err := rows.Scan(&w.id, &w.name, &w.url, &w.sel, &w.mkt, &w.lastHash, &w.lastText, &w.lastChecked, &w.lastChgd); err != nil {
			rows.Close()
			return nil, err
		}
		srcs = append(srcs, w)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	client := newWatchClient(a.m.InsecureWatch)
	var out []Proposal
	for _, w := range srcs {
		p, err := a.check(ctx, tx, client, w)
		if err != nil {
			return nil, err
		}
		if p != nil {
			out = append(out, *p)
		}
	}
	return out, nil
}

func (a regWatch) check(ctx context.Context, tx pgx.Tx, client *http.Client, w watchRow) (*Proposal, error) {
	fail := func(msg string) (*Proposal, error) {
		_, err := tx.Exec(ctx, `update watch_source set last_checked_at=now(), last_error=$2 where id=$1`, w.id, clip(msg, 500))
		return nil, err
	}
	body, mt, err := fetchWatched(ctx, client, w.url, a.m.InsecureWatch)
	if err != nil {
		return fail(err.Error())
	}
	text, err := normalizeText(body, mt, w.sel)
	if err != nil {
		return fail(err.Error())
	}
	hash := hashText(text)
	snippet := clip(text, maxLastText)
	if w.lastHash == "" || hash == w.lastHash {
		_, err := tx.Exec(ctx, `update watch_source set last_checked_at=now(), last_error='', last_hash=$2, last_text=case when last_hash='' then $3 else last_text end where id=$1`, w.id, hash, snippet)
		return nil, err
	}
	diff := unifiedDiff(w.lastText, snippet)
	if _, err := tx.Exec(ctx, `update watch_source set last_checked_at=now(), last_changed_at=now(), last_error='', last_hash=$2, last_text=$3 where id=$1`, w.id, hash, snippet); err != nil {
		return nil, err
	}
	if _, err := audit.Write(ctx, tx, audit.Record{Action: "watch.source.changed", Entity: "watch_source", EntityID: w.id.String(),
		Before: map[string]string{"hash": w.lastHash}, After: map[string]string{"hash": hash}}); err != nil {
		return nil, err
	}
	products, total, err := affectedProducts(ctx, tx, w.mkt)
	if err != nil {
		return nil, err
	}
	title := "Regulatory source changed: " + w.name
	reasoning := []string{fmt.Sprintf("The text of %s (%s) changed since the last check; hash %s -> %s.", w.name, w.url, w.lastHash[:12], hash[:12])}
	switch {
	case w.mkt == "":
		reasoning = append(reasoning, "The source has no market, so no products were matched.")
	case total == 0:
		reasoning = append(reasoning, fmt.Sprintf("No product in this workspace targets market %s.", w.mkt))
	default:
		reasoning = append(reasoning, fmt.Sprintf("%d products target market %s: %s.", total, w.mkt, strings.Join(products, ", ")))
	}
	notes := append([]string{}, reasoning...)
	notes = append(notes, "Diff excerpt:\n"+diff)
	in := caseInput{Kind: "regulatory.source_changed", Title: title, Priority: 2, Entity: "watch_source", EntityID: w.id.String(), Source: "reg-watch", SourceID: w.id.String() + "/" + hash[:16], Notes: notes}
	return &Proposal{Tool: "case.open", Title: title, Summary: "A watched regulatory source changed. Review the diff and decide whether the listed products are affected.",
		Reasoning: reasoning, Input: in, Preview: map[string]any{"source": w.name, "market": w.mkt, "products": products, "productCount": total, "diff": diff},
		Confidence: 0.9, Entity: "watch_source", EntityID: w.id.String(), DedupeKey: "regwatch:" + w.id.String() + ":" + hash[:16]}, nil
}

// affectedProducts lists (up to 50) the active products whose target markets include the market.
func affectedProducts(ctx context.Context, tx pgx.Tx, market string) ([]string, int, error) {
	if market == "" {
		return nil, 0, nil
	}
	rows, err := tx.Query(ctx, `select name || ' (' || gtin || ')' from product
		where status = 'active' and exists (select 1 from unnest(target_markets) tm where upper(tm) = upper($1)) order by name limit 50`, market)
	if err != nil {
		return nil, 0, err
	}
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			rows.Close()
			return nil, 0, err
		}
		out = append(out, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var total int
	err = tx.QueryRow(ctx, `select count(*) from product where status = 'active' and exists (select 1 from unnest(target_markets) tm where upper(tm) = upper($1))`, market).Scan(&total)
	return out, total, err
}

// --- watched sources: CRUD ---

// WatchSource is a configured source (the last text is not returned in full).
type WatchSource struct {
	ID            uuid.UUID  `json:"id"`
	Name          string     `json:"name"`
	URL           string     `json:"url"`
	Selector      string     `json:"selector"`
	Market        string     `json:"market"`
	Enabled       bool       `json:"enabled"`
	LastHash      string     `json:"lastHash"`
	LastPreview   string     `json:"lastPreview"`
	LastCheckedAt *time.Time `json:"lastCheckedAt,omitempty"`
	LastChangedAt *time.Time `json:"lastChangedAt,omitempty"`
	LastError     string     `json:"lastError"`
	CreatedAt     time.Time  `json:"createdAt"`
}

const watchCols = `id, name, url, selector, market, enabled, last_hash, left(last_text, 300), last_checked_at, last_changed_at, last_error, created_at`

func scanWatch(row pgx.Row) (WatchSource, error) {
	var s WatchSource
	err := row.Scan(&s.ID, &s.Name, &s.URL, &s.Selector, &s.Market, &s.Enabled, &s.LastHash, &s.LastPreview, &s.LastCheckedAt, &s.LastChangedAt, &s.LastError, &s.CreatedAt)
	return s, err
}

type watchInput struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Selector string `json:"selector"`
	Market   string `json:"market"`
	Enabled  *bool  `json:"enabled"`
}

var reMarket = regexp.MustCompile(`^[A-Z0-9-]{2,8}$`)

func (m *Module) validateWatch(in *watchInput) error {
	in.Name, in.URL, in.Selector = strings.TrimSpace(in.Name), strings.TrimSpace(in.URL), strings.TrimSpace(in.Selector)
	in.Market = strings.ToUpper(strings.TrimSpace(in.Market))
	if in.Name == "" || len(in.Name) > 120 {
		return httpx.BadRequest("name is required (at most 120 characters)")
	}
	if _, err := ValidateWatchURL(in.URL, m.InsecureWatch); err != nil {
		return httpx.BadRequest(err.Error())
	}
	if len(in.Selector) > 100 || !ValidSelector(in.Selector) {
		return httpx.BadRequest("selector must be empty or a tag, #id, .class, tag#id or tag.class")
	}
	if in.Market != "" && !reMarket.MatchString(in.Market) {
		return httpx.BadRequest("market must be a market code such as US or EU")
	}
	return nil
}

func (m *Module) watchRoutes(r chi.Router) {
	w := auth.Require(writerRoles...)
	r.Get("/watch-sources", httpx.Handler(m.listWatch))
	r.With(w).Post("/watch-sources", httpx.Handler(m.createWatch))
	r.With(w).Put("/watch-sources/{id}", httpx.Handler(m.updateWatch))
	r.With(w).Delete("/watch-sources/{id}", httpx.Handler(m.deleteWatch))
}

func (m *Module) listWatch(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `select `+watchCols+` from watch_source order by name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		items := []WatchSource{}
		for rows.Next() {
			s, err := scanWatch(rows)
			if err != nil {
				return err
			}
			items = append(items, s)
		}
		httpx.JSON(w, http.StatusOK, httpx.Page[WatchSource]{Items: items, Total: len(items)})
		return rows.Err()
	})
}

func (m *Module) createWatch(w http.ResponseWriter, r *http.Request) error {
	var in watchInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := m.validateWatch(&in); err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(r.Context(), `select count(*) from watch_source`).Scan(&n); err != nil {
			return err
		}
		if n >= maxSources {
			return httpx.Conflict(fmt.Sprintf("at most %d sources can be watched", maxSources))
		}
		s, err := scanWatch(tx.QueryRow(r.Context(), `insert into watch_source (id, tenant_id, name, url, selector, market, enabled) values ($1, app_tenant(), $2,$3,$4,$5,$6)
			on conflict (tenant_id, name) do nothing returning `+watchCols, ids.New(), in.Name, in.URL, in.Selector, in.Market, in.Enabled == nil || *in.Enabled))
		if err != nil {
			return httpx.Conflict("a source with this name already exists")
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "watch.source.create", Entity: "watch_source", EntityID: s.ID.String(), After: in}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, s)
		return nil
	})
}

func (m *Module) updateWatch(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in watchInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := m.validateWatch(&in); err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		before, err := scanWatch(tx.QueryRow(r.Context(), `select `+watchCols+` from watch_source where id=$1 for update`, id))
		if err != nil {
			return httpx.NotFound("unknown source")
		}
		// A different page or selector starts a new baseline.
		reset := before.URL != in.URL || before.Selector != in.Selector
		s, err := scanWatch(tx.QueryRow(r.Context(), `update watch_source set name=$2, url=$3, selector=$4, market=$5, enabled=$6,
			last_hash=case when $7 then '' else last_hash end, last_text=case when $7 then '' else last_text end,
			last_checked_at=case when $7 then null else last_checked_at end where id=$1 returning `+watchCols,
			id, in.Name, in.URL, in.Selector, in.Market, in.Enabled == nil || *in.Enabled, reset))
		if err != nil {
			return httpx.Conflict("a source with this name already exists")
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "watch.source.update", Entity: "watch_source", EntityID: id.String(), Before: before, After: s}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, s)
		return nil
	})
}

func (m *Module) deleteWatch(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		s, err := scanWatch(tx.QueryRow(r.Context(), `delete from watch_source where id=$1 returning `+watchCols, id))
		if err != nil {
			return httpx.NotFound("unknown source")
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "watch.source.delete", Entity: "watch_source", EntityID: id.String(), Before: s}); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}
