package app

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/act"
	"github.com/openspoor/openspoor/internal/dscsa"
	"github.com/openspoor/openspoor/internal/food"
	"github.com/openspoor/openspoor/internal/link"
)

// wireAgents hands the act module's agents the other modules' typed entry points as small
// interfaces (act may depend only on connectors and trace), so agent actions pass through the same
// functions and validators as the console.
func wireAgents(ac *act.Module, ds *dscsa.Module) {
	ac.Mapping = mappingStudio{}
	ac.Tracing = tracingAnswers{ds}
	ac.FSMA = fsmaSheet{}
}

type mappingStudio struct{}

func toSpec(def act.MappingDef) (link.MappingSpec, error) {
	var s link.MappingSpec
	b, err := json.Marshal(map[string]any{"docType": def.DocType, "format": def.Format, "options": rawOr(def.Options), "fields": def.Fields})
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(b, &s)
}

func rawOr(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage(`{}`)
	}
	return r
}

func (mappingStudio) ValidDocType(t string) bool { return link.ValidDocType(t) }

func (mappingStudio) Profile(def act.MappingDef, content []byte) (act.MappingProfile, error) {
	s, err := toSpec(def)
	if err != nil {
		return act.MappingProfile{}, err
	}
	s.Fields = nil
	res, err := link.ApplyMapping(s, content)
	return act.MappingProfile{Columns: res.Columns, Rows: res.Sample, Total: res.Rows}, err
}

func (mappingStudio) Suggest(docType string, columns []string) []act.MappingField {
	var out []act.MappingField
	b, _ := json.Marshal(link.SuggestMapping(docType, columns))
	_ = json.Unmarshal(b, &out)
	return out
}

func (mappingStudio) Check(def act.MappingDef) error {
	s, err := toSpec(def)
	if err != nil {
		return err
	}
	return link.ValidateSpec(s)
}

func (mappingStudio) EvalRow(def act.MappingDef, row map[string]string) (map[string]string, map[string]string) {
	s, err := toSpec(def)
	if err != nil {
		return nil, map[string]string{"": err.Error()}
	}
	return link.EvalRow(s, row)
}

func (mappingStudio) Save(ctx context.Context, tx pgx.Tx, def act.MappingDef) (uuid.UUID, error) {
	s, err := toSpec(def)
	if err != nil {
		return uuid.Nil, err
	}
	mp, err := link.SaveMapping(ctx, tx, link.MappingInput{Name: def.Name, PartnerID: def.PartnerID, DocType: s.DocType, Format: s.Format, Options: s.Options, Fields: s.Fields})
	return mp.ID, err
}

func (mappingStudio) Load(ctx context.Context, tx pgx.Tx, id uuid.UUID) (act.MappingDef, error) {
	mp, err := link.LoadMapping(ctx, tx, id)
	if err != nil {
		return act.MappingDef{}, err
	}
	var def act.MappingDef
	b, _ := json.Marshal(mp)
	err = json.Unmarshal(b, &def)
	return def, err
}

func (mappingStudio) Remove(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	return link.RemoveMapping(ctx, tx, id)
}

type tracingAnswers struct{ ds *dscsa.Module }

func (t tracingAnswers) Assemble(ctx context.Context, tx pgx.Tx, id uuid.UUID) (json.RawMessage, map[string]any, error) {
	return t.ds.AssembleTracingAnswer(ctx, tx, id)
}

func (t tracingAnswers) Preview(ctx context.Context, tx pgx.Tx, id uuid.UUID) (json.RawMessage, map[string]any, error) {
	return t.ds.PreviewTracingAnswer(ctx, tx, id)
}

type fsmaSheet struct{}

func (fsmaSheet) CSV(ctx context.Context, tx pgx.Tx, from, to time.Time, tlc string) (string, int, error) {
	var sb strings.Builder
	w := csv.NewWriter(&sb)
	n, err := food.FSMACSV(ctx, tx, w, from, to, tlc)
	w.Flush()
	return sb.String(), n, err
}
