package act

import (
	"context"
	"encoding/json"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/auth"
)

// The AI-native agents use other modules only through these small interfaces, set by
// internal/app (act may depend on connectors and trace only). Each interface is the same typed
// entry point the console uses, so an agent's action passes the same deterministic validators.

// MappingField maps one source column to a target field (same JSON shape as the mapping studio).
type MappingField struct {
	Target     string           `json:"target"`
	Source     string           `json:"source,omitempty"`
	Transforms []map[string]any `json:"transforms,omitempty"`
}

// MappingDef is a mapping definition.
type MappingDef struct {
	Name      string          `json:"name"`
	PartnerID *uuid.UUID      `json:"partnerId,omitempty"`
	DocType   string          `json:"docType"`
	Format    string          `json:"format"`
	Options   json.RawMessage `json:"options,omitempty"`
	Fields    []MappingField  `json:"fields"`
}

// MappingProfile describes a sample file: its columns and the first rows (column to value).
type MappingProfile struct {
	Columns []string
	Rows    []map[string]string
	Total   int
}

// MappingEngine is the link module's mapping studio.
type MappingEngine interface {
	ValidDocType(docType string) bool
	// Profile reads a sample file with the format and options of def (fields are ignored).
	Profile(def MappingDef, content []byte) (MappingProfile, error)
	// Suggest is the studio's header-based suggestion.
	Suggest(docType string, columns []string) []MappingField
	// Check validates a definition's shape (what saving enforces), without storing it.
	Check(def MappingDef) error
	// EvalRow resolves the mapped fields for one row: canonical value per target, and problems per target.
	EvalRow(def MappingDef, row map[string]string) (values, problems map[string]string)
	Save(ctx context.Context, tx pgx.Tx, def MappingDef) (uuid.UUID, error)
	Load(ctx context.Context, tx pgx.Tx, id uuid.UUID) (MappingDef, error)
	Remove(ctx context.Context, tx pgx.Tx, id uuid.UUID) error
}

// TracingAssembler is the dscsa module's tracing answer.
type TracingAssembler interface {
	// Assemble builds and stores the answer on an open investigation (no status change, no signature).
	Assemble(ctx context.Context, tx pgx.Tx, investigationID uuid.UUID) (answer json.RawMessage, summary map[string]any, err error)
	// Preview builds the answer without storing it.
	Preview(ctx context.Context, tx pgx.Tx, investigationID uuid.UUID) (answer json.RawMessage, summary map[string]any, err error)
}

// FSMAExporter is the food module's FSMA 204 sortable spreadsheet.
type FSMAExporter interface {
	// CSV writes the spreadsheet rows for events in [from, to) (optionally one lot and its lineage).
	CSV(ctx context.Context, tx pgx.Tx, from, to time.Time, tlc string) (csv string, rows int, err error)
}

// writerRoles may change data (Viewer is read-only).
var writerRoles = []string{auth.RoleAdmin, auth.RoleSerialization, auth.RoleCompliance, auth.RoleOps, auth.RoleIntegration}

func (m *Module) addAIAgents() {
	for _, t := range []Tool{mappingSaveTool{m}, investigationAnswerTool{m}, inspectionAssembleTool{m}} {
		m.tools[t.Name()] = t
	}
	m.agents = append(m.agents, mappingAgent{m}, onboardingHelper{}, investigationAssistant{}, inspectionAgent{}, regWatch{m})
}

func (m *Module) aiRoutes(r chi.Router) {
	m.mappingRoutes(r)
	m.inspectionRoutes(r)
	m.watchRoutes(r)
	m.documentRoutes(r)
	m.semanticRoutes(r)
	m.evalRoutes(r)
}
