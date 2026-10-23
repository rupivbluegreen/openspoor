package act

// The semantic catalogue: a declarative list of the metrics and dimensions the natural-language
// layer may ask for. A metric names an aggregate over one existing table; every dimension of a
// metric is a fixed column expression. Nothing in a request (or in a model's output) is ever
// spliced into SQL: the planner only picks entries from this list and binds values as parameters.
//
// Adding a metric is a code change reviewed like any other. The catalogue is validated when the
// module starts (identifiers, expressions and dimensions), and a test runs every entry against
// the schema.

// SemanticMetric is one measurable thing.
type SemanticMetric struct {
	ID          string
	Name        string
	Description string
	Unit        string // count | quantity | % | h ...
	Table       string // FROM clause: a table name, nothing else
	TimeColumn  string // the column the period applies to
	Agg         string // aggregate expression over the table, e.g. count(*) or sum(quantity)
	Where       string // optional fixed predicate (no user input)
	// Dims maps a dimension id to the column expression it groups by.
	Dims     map[string]string
	Synonyms []string
	// KPI is set for metrics computed by the KPI library (wired by internal/app); such a metric
	// supports no dimension except day.
	KPI string
}

// SemanticDimension describes a grouping for display and for reading questions.
type SemanticDimension struct {
	ID          string
	Name        string
	Description string
	Synonyms    []string
	Time        bool // a time bucket (day, week, month)
}

// semanticDimensions lists every dimension id a metric may use.
var semanticDimensions = []SemanticDimension{
	{ID: "day", Name: "Day", Description: "Calendar day (UTC)", Synonyms: []string{"daily", "per day", "each day"}, Time: true},
	{ID: "week", Name: "Week", Description: "Calendar week starting Monday (UTC)", Synonyms: []string{"weekly", "per week"}, Time: true},
	{ID: "month", Name: "Month", Description: "Calendar month (UTC)", Synonyms: []string{"monthly", "per month"}, Time: true},
	{ID: "event_type", Name: "Event type", Description: "EPCIS event type", Synonyms: []string{"type of event"}},
	{ID: "biz_step", Name: "Business step", Description: "EPCIS business step", Synonyms: []string{"business step", "step"}},
	{ID: "disposition", Name: "Disposition", Description: "Disposition of the packs or event", Synonyms: []string{}},
	{ID: "action", Name: "Action", Description: "EPCIS event action", Synonyms: []string{}},
	{ID: "status", Name: "Status", Description: "Record status", Synonyms: []string{"state"}},
	{ID: "gtin", Name: "GTIN", Description: "Product GTIN", Synonyms: []string{"product"}},
	{ID: "lot", Name: "Lot", Description: "Lot or batch number", Synonyms: []string{"batch"}},
	{ID: "kind", Name: "Kind", Description: "Kind of record", Synonyms: []string{"category"}},
	{ID: "severity", Name: "Severity", Description: "Alert severity", Synonyms: []string{}},
	{ID: "priority", Name: "Priority", Description: "Case priority (1 is most urgent)", Synonyms: []string{}},
	{ID: "agent", Name: "Agent", Description: "Governed agent", Synonyms: []string{}},
	{ID: "tool", Name: "Tool", Description: "Agent tool (action type)", Synonyms: []string{"action type"}},
	{ID: "risk", Name: "Risk", Description: "Risk class of an action", Synonyms: []string{}},
	{ID: "connector", Name: "Connector", Description: "Regulatory connector", Synonyms: []string{"hub", "market"}},
	{ID: "direction", Name: "Direction", Description: "Inbound or outbound", Synonyms: []string{}},
	{ID: "type", Name: "Document type", Description: "Document type", Synonyms: []string{"document type"}},
	{ID: "uom", Name: "Unit of measure", Description: "Unit of measure", Synonyms: []string{"unit"}},
	{ID: "origin", Name: "Origin", Description: "How the lot came about", Synonyms: []string{}},
}

// semanticMetrics is the catalogue of SQL-backed metrics. Every table has a tenant_id column; the
// planner always adds tenant_id = app_tenant() on top of row-level security.
var semanticMetrics = []SemanticMetric{
	{ID: "epcis_events", Name: "EPCIS events", Description: "Number of EPCIS events by event time.", Unit: "count",
		Table: "epcis_event", TimeColumn: "event_time", Agg: "count(*)",
		Dims:     map[string]string{"event_type": "event_type", "biz_step": "biz_step", "disposition": "disposition", "action": "action"},
		Synonyms: []string{"events", "epcis events", "traceability events", "captured events"}},
	{ID: "serial_updates", Name: "Serial number updates", Description: "Number of serial numbers whose last event falls in the period.", Unit: "count",
		Table: "serial_state", TimeColumn: "last_event_time", Agg: "count(*)",
		Dims:     map[string]string{"status": "status", "gtin": "gtin", "lot": "lot", "disposition": "disposition"},
		Synonyms: []string{"serials", "serial numbers", "packs", "serial updates"}},
	{ID: "alerts", Name: "Alerts", Description: "Number of alerts raised.", Unit: "count",
		Table: "alert", TimeColumn: "created_at", Agg: "count(*)",
		Dims:     map[string]string{"kind": "kind", "severity": "severity", "status": "status"},
		Synonyms: []string{"alerts", "exceptions", "warnings"}},
	{ID: "cases", Name: "Work cases", Description: "Number of work cases opened.", Unit: "count",
		Table: "work_case", TimeColumn: "created_at", Agg: "count(*)",
		Dims:     map[string]string{"kind": "kind", "status": "status", "priority": "priority::text"},
		Synonyms: []string{"cases", "work cases", "tasks", "work items"}},
	{ID: "agent_actions", Name: "Agent actions", Description: "Number of actions proposed by governed agents.", Unit: "count",
		Table: "agent_action", TimeColumn: "created_at", Agg: "count(*)",
		Dims:     map[string]string{"agent": "agent", "tool": "tool", "status": "status", "risk": "risk"},
		Synonyms: []string{"agent actions", "proposals", "agent proposals", "actions"}},
	{ID: "regulatory_reports", Name: "Regulatory reports", Description: "Number of regulatory reports created.", Unit: "count",
		Table: "regulatory_report", TimeColumn: "created_at", Agg: "count(*)",
		Dims:     map[string]string{"connector": "connector", "kind": "kind", "status": "status"},
		Synonyms: []string{"reports", "regulatory reports", "submissions", "hub reports"}},
	{ID: "shipments", Name: "Shipments", Description: "Number of shipments created.", Unit: "count",
		Table: "shipment", TimeColumn: "created_at", Agg: "count(*)",
		Dims:     map[string]string{"direction": "direction", "status": "status"},
		Synonyms: []string{"shipments", "despatches", "deliveries"}},
	{ID: "investigations", Name: "Investigations", Description: "Number of investigations opened.", Unit: "count",
		Table: "investigation", TimeColumn: "created_at", Agg: "count(*)",
		Dims:     map[string]string{"kind": "kind", "status": "status"},
		Synonyms: []string{"investigations", "tracing requests", "suspect product cases"}},
	{ID: "b2b_documents", Name: "Partner documents", Description: "Number of orders, advices, invoices and other B2B documents.", Unit: "count",
		Table: "b2b_document", TimeColumn: "created_at", Agg: "count(*)",
		Dims:     map[string]string{"type": "type", "direction": "direction", "status": "status"},
		Synonyms: []string{"documents", "partner documents", "b2b documents", "orders and invoices"}},
	{ID: "food_lot_quantity", Name: "Food lot quantity", Description: "Total quantity of food traceability lots created. Quantities in different units are shown apart: group by unit of measure.", Unit: "quantity",
		Table: "food_lot", TimeColumn: "created_at", Agg: "coalesce(sum(quantity), 0)",
		Dims:     map[string]string{"status": "status", "uom": "uom", "origin": "origin"},
		Synonyms: []string{"food lots", "lot quantity", "lots"}},
}
