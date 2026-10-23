package act

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
	"github.com/openspoor/openspoor/pkg/doctext"
)

// Document reading without external services. An uploaded PDF (text layer only), CSV, XML, HTML or
// plain-text file is reduced to text by pkg/doctext within fixed size limits; only the extracted
// text is stored (passages in library_chunk, per tenant, under row-level security). Search uses
// PostgreSQL full-text search (tsvector, GIN index), so it needs no extension. The assistant cites
// passages as library/<id>#<seq>.
//
// TODO(spec): OCR for scanned documents is not built; a PDF without a text layer is refused.
// TODO(spec): the text search configuration is 'simple' (no stemming, no stop words) so English,
// Dutch and Arabic share one index; a per-language configuration is not decided.

const (
	maxDocumentBytes = 5 << 20 // uploaded file
	chunkBytes       = 1200    // passage size
	maxChunksPerDoc  = 2000
)

// Document is the metadata of an uploaded document.
type Document struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Format    string    `json:"format"`
	MediaType string    `json:"mediaType"`
	SizeBytes int       `json:"sizeBytes"`
	SHA256    string    `json:"sha256"`
	Pages     int       `json:"pages"`
	Chars     int       `json:"chars"`
	Passages  int       `json:"passages"`
	Truncated bool      `json:"truncated"`
	Warnings  []string  `json:"warnings"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
}

const docCols = `d.id, d.name, d.format, d.media_type, d.size_bytes, d.sha256, d.pages, d.chars, d.truncated, d.warnings, d.created_by, d.created_at,
	(select count(*) from library_chunk c where c.document_id = d.id)`

func scanDocument(row pgx.Row) (Document, error) {
	var d Document
	err := row.Scan(&d.ID, &d.Name, &d.Format, &d.MediaType, &d.SizeBytes, &d.SHA256, &d.Pages, &d.Chars, &d.Truncated, &d.Warnings, &d.CreatedBy, &d.CreatedAt, &d.Passages)
	if errors.Is(err, pgx.ErrNoRows) {
		err = db.ErrNotFound
	}
	if d.Warnings == nil {
		d.Warnings = []string{}
	}
	return d, err
}

// Passage is a cited piece of a document.
type Passage struct {
	DocumentID uuid.UUID `json:"documentId"`
	Document   string    `json:"document"`
	Seq        int       `json:"seq"`
	Ref        string    `json:"ref"`
	Text       string    `json:"text"`
	Rank       float64   `json:"rank,omitempty"`
}

func (m *Module) libraryRoutes(r chi.Router) {
	r.Get("/library", httpx.Handler(m.listDocuments))
	r.Get("/library/search", httpx.Handler(m.searchDocumentsHandler))
	r.Get("/library/{id}", httpx.Handler(m.getDocument))
	r.With(auth.Require(writerRoles...)).Post("/library", httpx.Handler(m.uploadDocument))
	r.With(auth.Require(writerRoles...)).Delete("/library/{id}", httpx.Handler(m.deleteDocument))
}

func (m *Module) uploadDocument(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name          string `json:"name"`
		MediaType     string `json:"mediaType"`
		Content       string `json:"content"`
		ContentBase64 string `json:"contentBase64"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 200 {
		return httpx.BadRequest("name is required (at most 200 characters)")
	}
	data := []byte(in.Content)
	if in.ContentBase64 != "" {
		var err error
		if data, err = base64.StdEncoding.DecodeString(in.ContentBase64); err != nil {
			return httpx.BadRequest("contentBase64 must be base64")
		}
	}
	if len(data) == 0 {
		return httpx.BadRequest("content is required")
	}
	lim := doctext.DefaultLimits
	lim.MaxBytes = maxDocumentBytes
	res, err := doctext.Extract(in.Name, in.MediaType, data, lim)
	switch {
	case errors.Is(err, doctext.ErrTooLarge):
		return &httpx.Error{Status: http.StatusRequestEntityTooLarge, Msg: "a document may be at most 5 MiB"}
	case err != nil:
		return httpx.BadRequest(err.Error())
	}
	chunks := doctext.Chunks(res.Text, chunkBytes)
	if len(chunks) > maxChunksPerDoc {
		chunks = chunks[:maxChunksPerDoc]
		res.Truncated = true
		res.Warnings = append(res.Warnings, "only the first passages are indexed")
	}
	if res.Warnings == nil {
		res.Warnings = []string{}
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	id := ids.New()
	p := auth.MustFrom(r.Context())
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		var dup uuid.UUID
		if err := tx.QueryRow(r.Context(), `select id from library_document where sha256=$1`, hash).Scan(&dup); err == nil {
			return httpx.Conflict("this file is already uploaded as document " + dup.String())
		}
		if _, err := tx.Exec(r.Context(), `insert into library_document (id, tenant_id, name, format, media_type, size_bytes, sha256, pages, chars, truncated, warnings, created_by)
			values ($1, app_tenant(), $2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			id, in.Name, string(res.Format), in.MediaType, len(data), hash, res.Pages, len(res.Text), res.Truncated, res.Warnings, p.Actor()); err != nil {
			return err
		}
		for i, c := range chunks {
			if _, err := tx.Exec(r.Context(), `insert into library_chunk (id, tenant_id, document_id, seq, body) values ($1, app_tenant(), $2,$3,$4)`, ids.New(), id, i, c); err != nil {
				return err
			}
		}
		d, err := scanDocument(tx.QueryRow(r.Context(), `select `+docCols+` from library_document d where d.id=$1`, id))
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "library.upload", Entity: "library_document", EntityID: id.String(), After: d}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, d)
		return nil
	})
}

func (m *Module) listDocuments(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `select `+docCols+` from library_document d order by d.created_at desc limit 200`)
		if err != nil {
			return err
		}
		defer rows.Close()
		out := []Document{}
		for rows.Next() {
			d, err := scanDocument(rows)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, httpx.Page[Document]{Items: out, Total: len(out)})
		return nil
	})
}

func (m *Module) getDocument(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		d, err := scanDocument(tx.QueryRow(r.Context(), `select `+docCols+` from library_document d where d.id=$1`, id))
		if err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `select seq, body from library_chunk where document_id=$1 order by seq limit 50`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		ps := []Passage{}
		for rows.Next() {
			p := Passage{DocumentID: id, Document: d.Name}
			if err := rows.Scan(&p.Seq, &p.Text); err != nil {
				return err
			}
			p.Ref = passageRef(id, p.Seq)
			ps = append(ps, p)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"document": d, "passages": ps})
		return nil
	})
}

func (m *Module) deleteDocument(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		d, err := scanDocument(tx.QueryRow(r.Context(), `select `+docCols+` from library_document d where d.id=$1`, id))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `delete from library_document where id=$1`, id); err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "library.delete", Entity: "library_document", EntityID: id.String(), Before: d}); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}

func (m *Module) searchDocumentsHandler(w http.ResponseWriter, r *http.Request) error {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(q)) < 2 || len(q) > 200 {
		return httpx.BadRequest("q must be 2 to 200 characters")
	}
	limit := httpx.IntQuery(r, "limit", 10, 1, 50)
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		ps, err := searchDocuments(r.Context(), tx, q, limit)
		if err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, httpx.Page[Passage]{Items: ps, Total: len(ps)})
		return nil
	})
}

func passageRef(id uuid.UUID, seq int) string {
	return "library/" + id.String() + "#" + strconv.Itoa(seq)
}

// searchDocuments runs a full-text query over the tenant's passages (RLS-scoped through tx). q uses
// web-search syntax (quoted phrases, "or", "-") and is always bound as a parameter.
// The text is a highlighted excerpt (matches in [[ ]]), not the whole passage.
func searchDocuments(ctx context.Context, tx pgx.Tx, q string, limit int) ([]Passage, error) {
	rows, err := tx.Query(ctx, `select d.id, d.name, c.seq,
			ts_headline('simple', c.body, query, 'StartSel=[[,StopSel=]],MaxWords=40,MinWords=15,MaxFragments=1'), ts_rank(c.tsv, query)
		from library_chunk c join library_document d on d.id = c.document_id, websearch_to_tsquery('simple', $1) query
		where c.tsv @@ query order by ts_rank(c.tsv, query) desc, d.created_at desc, c.seq limit $2`, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Passage{}
	for rows.Next() {
		var p Passage
		if err := rows.Scan(&p.DocumentID, &p.Document, &p.Seq, &p.Text, &p.Rank); err != nil {
			return nil, err
		}
		p.Ref = passageRef(p.DocumentID, p.Seq)
		out = append(out, p)
	}
	return out, rows.Err()
}

var questionStop = map[string]bool{"the": true, "and": true, "for": true, "what": true, "which": true, "who": true, "how": true, "does": true, "are": true,
	"was": true, "were": true, "with": true, "that": true, "this": true, "from": true, "have": true, "has": true, "our": true, "your": true, "about": true,
	"there": true, "show": true, "tell": true, "list": true, "many": true, "much": true, "any": true, "all": true, "can": true, "should": true, "into": true}

var reNonWord = regexp.MustCompile(`[^\p{L}\p{N}-]+`)

// questionQuery turns a natural-language question into an "a or b or c" query over its content
// words (at most 12; letters and digits only, so no search operator can come from the question).
func questionQuery(question string) string {
	var words []string
	seen := map[string]bool{}
	for _, w := range reNonWord.Split(strings.ToLower(question), -1) {
		w = strings.Trim(w, "-")
		if len([]rune(w)) < 3 || questionStop[w] || seen[w] || strings.IndexFunc(w, unicode.IsLetter) < 0 && len(w) < 4 {
			continue
		}
		seen[w] = true
		words = append(words, w)
		if len(words) == 12 {
			break
		}
	}
	return strings.Join(words, " or ")
}
