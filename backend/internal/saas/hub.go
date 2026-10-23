package saas

import (
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/config"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
)

// Hub analytics serve a regulator running a national repository (OPENSPOOR_MODE=hub): the
// reporting organisations are tenants, and the operator sees the market across them. In SaaS
// and single mode the operator is a hosting provider, so the endpoint does not exist there.

type hubOrg struct {
	TenantID     string     `json:"tenantId" db:"tenant_id"`
	Name         string     `json:"name" db:"name"`
	Captures     int64      `json:"captures" db:"captures"`
	Events       int64      `json:"events" db:"events"`
	Rejected     int64      `json:"rejectedCaptures" db:"rejected"`
	LastReportAt *time.Time `json:"lastReportAt" db:"last_report_at"`
	DaysSilent   *int       `json:"daysSilent" db:"days_silent"`
}

type hubCount struct {
	Key   string `json:"key" db:"key"`
	Count int64  `json:"count" db:"count"`
}

type hubProduct struct {
	GTIN        string `json:"gtin" db:"gtin"`
	Name        string `json:"name" db:"name"`
	Owner       string `json:"owner" db:"owner"`
	Serials     int64  `json:"serials" db:"serials"`
	FailedScans int64  `json:"failedScans" db:"failed_scans"`
	FailedVRS   int64  `json:"failedVerifications" db:"failed_vrs"`
}

func (m *Module) hubAnalytics(w http.ResponseWriter, r *http.Request) error {
	if m.Cfg.Mode != config.ModeHub {
		return httpx.NotFound("market analytics are available in hub mode only")
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 366 {
		days = 30
	}
	var orgs []hubOrg
	var states, alerts []hubCount
	var products []hubProduct
	err := m.DB.OperatorTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		if orgs, err = db.List[hubOrg](r.Context(), tx, `select t.id::text as tenant_id, t.name,
				count(c.id) as captures, coalesce(sum(c.event_count) filter (where c.status <> 'rejected'), 0)::bigint as events,
				count(c.id) filter (where c.status = 'rejected') as rejected,
				max(c.created_at) as last_report_at,
				(current_date - max(c.created_at)::date) as days_silent
			from tenant t left join epcis_capture c on c.tenant_id = t.id and c.created_at > now() - make_interval(days => $1)
			where t.status = 'active' and not exists (select 1 from app_user u where u.tenant_id = t.id and u.operator)
			group by t.id, t.name order by events desc, t.name`, days); err != nil {
			return err
		}
		if states, err = db.List[hubCount](r.Context(), tx, `select status as key, count(*) as count from serial_state group by status order by count desc`); err != nil {
			return err
		}
		if alerts, err = db.List[hubCount](r.Context(), tx, `select kind as key, count(*) as count from alert
			where status = 'open' and created_at > now() - make_interval(days => $1) group by kind order by count desc limit 20`, days); err != nil {
			return err
		}
		products, err = db.List[hubProduct](r.Context(), tx, `with s as (select tenant_id, gtin, count(*) as n from serial_state group by tenant_id, gtin),
				f as (select tenant_id, gtin, sum(count) as n from scan_stat where outcome not in ('verified') and day > current_date - $1::int group by tenant_id, gtin),
				v as (select tenant_id, gtin, count(*) as n from vrs_request where verified = false and created_at > now() - make_interval(days => $1) group by tenant_id, gtin)
			select p.gtin, p.name, t.name as owner, coalesce(s.n, 0)::bigint as serials, coalesce(f.n, 0)::bigint as failed_scans, coalesce(v.n, 0)::bigint as failed_vrs
			from product p join tenant t on t.id = p.tenant_id
			left join s on s.tenant_id = p.tenant_id and s.gtin = p.gtin
			left join f on f.tenant_id = p.tenant_id and f.gtin = p.gtin
			left join v on v.tenant_id = p.tenant_id and v.gtin = p.gtin
			where not exists (select 1 from app_user u where u.tenant_id = t.id and u.operator)
			order by (coalesce(f.n, 0) + coalesce(v.n, 0)) desc, serials desc limit 100`, days)
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"days": days, "organisations": orgs, "serialStates": states, "openAlerts": alerts, "products": products})
	return nil
}
