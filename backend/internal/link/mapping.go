package link

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
	"github.com/openspoor/openspoor/internal/partners"
	lib "github.com/openspoor/openspoor/pkg/epcis"
)

// Mapping is a saved mapping for a partner and document type.
type Mapping struct {
	ID        uuid.UUID      `json:"id" db:"id"`
	Name      string         `json:"name" db:"name"`
	PartnerID *uuid.UUID     `json:"partnerId,omitempty" db:"partner_id"`
	DocType   string         `json:"docType" db:"doc_type"`
	Format    string         `json:"format" db:"format"`
	Options   MappingOptions `json:"options" db:"options"`
	Fields    []MappingField `json:"fields" db:"fields"`
	CreatedAt time.Time      `json:"createdAt" db:"created_at"`
	UpdatedAt time.Time      `json:"updatedAt" db:"updated_at"`
}

const mappingCols = `id, name, partner_id, doc_type, format, options, fields, created_at, updated_at`

// Spec returns the mapping definition.
func (m Mapping) Spec() MappingSpec {
	return MappingSpec{DocType: m.DocType, Format: m.Format, Options: m.Options, Fields: m.Fields}
}

type mappingInput struct {
	Name      string         `json:"name"`
	PartnerID *uuid.UUID     `json:"partnerId"`
	DocType   string         `json:"docType"`
	Format    string         `json:"format"`
	Options   MappingOptions `json:"options"`
	Fields    []MappingField `json:"fields"`
}

func (in *mappingInput) validate(ctx context.Context, tx pgx.Tx) error {
	if strings.TrimSpace(in.Name) == "" {
		return httpx.BadRequest("name is required")
	}
	s := MappingSpec{DocType: in.DocType, Format: in.Format, Options: in.Options, Fields: in.Fields}
	if err := s.Validate(); err != nil {
		return httpx.BadRequest(err.Error())
	}
	if in.PartnerID != nil {
		if _, err := partners.Get(ctx, tx, *in.PartnerID); err != nil {
			return httpx.BadRequest("unknown partnerId")
		}
	}
	return nil
}

func (m *Module) listMappings(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[Mapping](r.Context(), tx, `select `+mappingCols+` from b2b_mapping
			where ($1='' or partner_id::text=$1) and ($2='' or doc_type=$2) order by name`, q.Get("partnerId"), q.Get("docType"))
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[Mapping]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) getMapping(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		mp, err := db.One[Mapping](r.Context(), tx, `select `+mappingCols+` from b2b_mapping where id=$1`, id)
		if err == nil {
			httpx.JSON(w, http.StatusOK, mp)
		}
		return err
	})
}

func (m *Module) createMapping(w http.ResponseWriter, r *http.Request) error {
	var in mappingInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		if err := in.validate(r.Context(), tx); err != nil {
			return err
		}
		mp, err := db.One[Mapping](r.Context(), tx, `insert into b2b_mapping (id, tenant_id, name, partner_id, doc_type, format, options, fields)
			values ($1, app_tenant(), $2,$3,$4,$5,$6,$7) returning `+mappingCols, ids.New(), strings.TrimSpace(in.Name), in.PartnerID, in.DocType, in.Format, in.Options, in.Fields)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "b2b.mapping.create", Entity: "b2b_mapping", EntityID: mp.ID.String(), After: mp}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, mp)
		return nil
	})
}

func (m *Module) updateMapping(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in mappingInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		if err := in.validate(r.Context(), tx); err != nil {
			return err
		}
		before, err := db.One[Mapping](r.Context(), tx, `select `+mappingCols+` from b2b_mapping where id=$1`, id)
		if err != nil {
			return err
		}
		mp, err := db.One[Mapping](r.Context(), tx, `update b2b_mapping set name=$2, partner_id=$3, doc_type=$4, format=$5, options=$6, fields=$7, updated_at=now()
			where id=$1 returning `+mappingCols, id, strings.TrimSpace(in.Name), in.PartnerID, in.DocType, in.Format, in.Options, in.Fields)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "b2b.mapping.update", Entity: "b2b_mapping", EntityID: id.String(), Before: before, After: mp}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, mp)
		return nil
	})
}

func (m *Module) deleteMapping(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		before, err := db.One[Mapping](r.Context(), tx, `delete from b2b_mapping where id=$1 returning `+mappingCols, id)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "b2b.mapping.delete", Entity: "b2b_mapping", EntityID: id.String(), Before: before}); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}

// fileContent accepts text content or base64 content.
func fileContent(text, b64 string) ([]byte, error) {
	if b64 != "" {
		b, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, httpx.BadRequest("contentBase64 must be base64")
		}
		return b, nil
	}
	if text == "" {
		return nil, httpx.BadRequest("content is required")
	}
	return []byte(text), nil
}

// previewMapping applies a saved or inline mapping to a sample file without storing anything.
func (m *Module) previewMapping(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		MappingID *uuid.UUID `json:"mappingId"`
		Mapping   *struct {
			MappingSpec
			Name      string     `json:"name"`
			PartnerID *uuid.UUID `json:"partnerId"`
		} `json:"mapping"`
		Content       string `json:"content"`
		ContentBase64 string `json:"contentBase64"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	content, err := fileContent(in.Content, in.ContentBase64)
	if err != nil {
		return err
	}
	var spec MappingSpec
	switch {
	case in.MappingID != nil:
		err := m.Tx(r.Context(), func(tx pgx.Tx) error {
			mp, err := db.One[Mapping](r.Context(), tx, `select `+mappingCols+` from b2b_mapping where id=$1`, *in.MappingID)
			spec = mp.Spec()
			return err
		})
		if err != nil {
			return err
		}
	case in.Mapping != nil:
		spec = in.Mapping.MappingSpec
		// A preview may run before any field is mapped: only the file shape is required then.
		if len(spec.Fields) > 0 {
			if err := spec.Validate(); err != nil {
				return httpx.BadRequest(err.Error())
			}
		} else if spec.Format != "csv" && spec.Format != "json" && spec.Format != "flat" {
			return httpx.BadRequest("format must be csv, json or flat")
		}
	default:
		return httpx.BadRequest("mappingId or mapping is required")
	}
	res, err := ApplyMapping(spec, content)
	if err != nil {
		return httpx.BadRequest(err.Error())
	}
	httpx.JSON(w, http.StatusOK, res)
	return nil
}

func (m *Module) suggestMapping(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		DocType string   `json:"docType"`
		Columns []string `json:"columns"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if !validDocType(in.DocType) {
		return httpx.BadRequest("unknown docType")
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"fields": SuggestMapping(in.DocType, in.Columns), "targets": MappingTargets(in.DocType)})
	return nil
}

// importMapped applies a saved mapping to an inbound file and stores the result: B2B
// documents (all or nothing) or one EPCIS capture.
func (m *Module) importMapped(w http.ResponseWriter, r *http.Request, mappingID, partnerID *uuid.UUID, format string, content []byte) error {
	ctx := r.Context()
	var mp Mapping
	err := m.Tx(ctx, func(tx pgx.Tx) error {
		var err error
		if mappingID != nil {
			mp, err = db.One[Mapping](ctx, tx, `select `+mappingCols+` from b2b_mapping where id=$1`, *mappingID)
			if errors.Is(err, db.ErrNotFound) {
				return httpx.BadRequest("unknown mappingId")
			}
			return err
		}
		if partnerID == nil {
			return httpx.BadRequest("mappingId or partnerId is required for " + format + " files")
		}
		list, err := db.List[Mapping](ctx, tx, `select `+mappingCols+` from b2b_mapping where partner_id=$1 and format=$2 order by updated_at desc`, *partnerID, format)
		if err != nil {
			return err
		}
		if len(list) != 1 {
			return httpx.BadRequest("the partner needs exactly one saved " + format + " mapping, or pass mappingId")
		}
		mp = list[0]
		return nil
	})
	if err != nil {
		return err
	}
	if partnerID == nil {
		partnerID = mp.PartnerID
	}
	res, err := ApplyMapping(mp.Spec(), content)
	if err != nil {
		return httpx.BadRequest(err.Error())
	}
	if len(res.Errors) > 0 {
		return httpx.Unprocessable("the file does not map cleanly; nothing was stored", res.Errors)
	}
	if mp.DocType == "epcis" {
		if len(res.Events) == 0 {
			return httpx.BadRequest("the file has no rows")
		}
		body, err := lib.WriteJSON(lib.NewDocument(res.Events))
		if err != nil {
			return err
		}
		c, err := m.EPCIS.Capture(ctx, "application/ld+json", body, "mapping:"+mp.Name)
		if err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, map[string]any{"mappingId": mp.ID, "captureId": c.ID, "events": len(res.Events)})
		return nil
	}
	if len(res.Documents) == 0 {
		return httpx.BadRequest("the file has no rows")
	}
	return m.Tx(ctx, func(tx pgx.Tx) error {
		docs := make([]Doc, 0, len(res.Documents))
		for _, d := range res.Documents {
			doc, err := StoreDoc(ctx, tx, "inbound", partnerID, nil, d)
			if err != nil {
				return err
			}
			docs = append(docs, doc)
		}
		if _, err := audit.Write(ctx, tx, audit.Record{Action: "b2b.mapping.apply", Entity: "b2b_mapping", EntityID: mp.ID.String(),
			After: map[string]any{"documents": len(docs), "rows": res.Rows}}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, map[string]any{"mappingId": mp.ID, "documents": docs})
		return nil
	})
}
