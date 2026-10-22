// Package serialization owns serial pools per GTIN+market, the idempotent allocation API
// for Level 3 systems, GS1 DataMatrix / Digital Link encoding and decoding, commissioned-serial
// bitmaps (double-commission detection) and pool exhaustion alerts.
package serialization

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"image/png"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/RoaringBitmap/roaring/v2/roaring64"
	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/datamatrix"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
	"github.com/openspoor/openspoor/internal/core/kernel"
	"github.com/openspoor/openspoor/internal/masterdata"
	epcislib "github.com/openspoor/openspoor/pkg/epcis"
	"github.com/openspoor/openspoor/pkg/gs1"
)

// MaxAllocation is the largest single allocation request.
const MaxAllocation = 1_000_000

// Module is the serialization bounded context.
type Module struct {
	kernel.Deps
	gens sync.Map // pool id -> *Generator
}

// New creates the module.
func New(d kernel.Deps) *Module { return &Module{Deps: d} }

// Name implements kernel.Module.
func (m *Module) Name() string { return "serialization" }

// Pool is a serial number pool for one GTIN in one market.
type Pool struct {
	ID              uuid.UUID `json:"id" db:"id"`
	ProductID       uuid.UUID `json:"productId" db:"product_id"`
	GTIN            string    `json:"gtin" db:"gtin"`
	Market          string    `json:"market" db:"market"`
	Mode            string    `json:"mode" db:"mode"`
	Alphabet        string    `json:"alphabet" db:"alphabet"`
	Length          int       `json:"length" db:"length"`
	Prefix          string    `json:"prefix" db:"prefix"`
	Issued          int64     `json:"issued" db:"next_counter"`
	Capacity        int64     `json:"capacity" db:"capacity"`
	LowWatermarkPct int       `json:"lowWatermarkPct" db:"low_watermark_pct"`
	Status          string    `json:"status" db:"status"`
	CreatedAt       time.Time `json:"createdAt" db:"created_at"`
	Remaining       int64     `json:"remaining" db:"-"`
}

const poolCols = `id, product_id, gtin, market, mode, alphabet, length, prefix, next_counter, capacity, low_watermark_pct, status, created_at`

func (p *Pool) fill() { p.Remaining = p.Capacity - p.Issued }

// Allocation is the result of an allocation request.
type Allocation struct {
	AllocationID uuid.UUID `json:"allocationId"`
	PoolID       uuid.UUID `json:"poolId"`
	GTIN         string    `json:"gtin"`
	Start        int64     `json:"start"`
	Count        int       `json:"count"`
	Serials      []string  `json:"serials"`
	Replayed     bool      `json:"replayed"`
}

// Routes implements kernel.Module.
func (m *Module) Routes(r chi.Router) {
	r.Route("/serial-pools", func(r chi.Router) {
		r.Use(auth.Require(auth.RoleAdmin, auth.RoleSerialization, auth.RoleIntegration))
		r.Get("/", httpx.Handler(m.listPools))
		r.Post("/", httpx.Handler(m.createPool))
		r.Get("/{id}", httpx.Handler(m.getPool))
		r.Post("/{id}/allocate", httpx.Handler(m.allocate))
	})
	r.Post("/codes/encode", httpx.Handler(m.encode))
	r.Post("/codes/decode", httpx.Handler(m.decode))
}

func (m *Module) listPools(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[Pool](r.Context(), tx, `select `+poolCols+` from serial_pool order by created_at desc`)
		for i := range items {
			items[i].fill()
		}
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[Pool]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) getPool(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		p, err := db.One[Pool](r.Context(), tx, `select `+poolCols+` from serial_pool where id=$1`, id)
		p.fill()
		if err == nil {
			httpx.JSON(w, http.StatusOK, p)
		}
		return err
	})
}

// PoolInput creates a pool.
type PoolInput struct {
	ProductID       uuid.UUID `json:"productId"`
	Market          string    `json:"market"`
	Mode            string    `json:"mode"`
	Alphabet        string    `json:"alphabet"`
	Length          int       `json:"length"`
	Prefix          string    `json:"prefix"`
	LowWatermarkPct int       `json:"lowWatermarkPct"`
}

// CreatePool creates a pool inside tx.
func (m *Module) CreatePool(ctx context.Context, tx pgx.Tx, in PoolInput) (Pool, error) {
	if in.Mode == "" {
		in.Mode = "random"
	}
	if in.Alphabet == "" {
		if in.Mode == "sequential" {
			in.Alphabet = "0123456789"
		} else {
			in.Alphabet = DefaultAlphabet
		}
	}
	if in.Length == 0 {
		in.Length = 12
	}
	if in.LowWatermarkPct == 0 {
		in.LowWatermarkPct = 10
	}
	in.Market = strings.ToUpper(strings.TrimSpace(in.Market))
	if in.Market == "" {
		return Pool{}, httpx.BadRequest("market is required (e.g. US, EU, SA)")
	}
	prod, err := masterdata.ByID(ctx, tx, in.ProductID)
	if err != nil {
		return Pool{}, httpx.BadRequest("unknown productId")
	}
	key := make([]byte, 16)
	_, _ = rand.Read(key)
	id := ids.New()
	g, err := NewGenerator(in.Mode, in.Alphabet, in.Length, in.Prefix, key, tweakFor(prod.GTIN, in.Market))
	if err != nil {
		return Pool{}, httpx.BadRequest(err.Error())
	}
	sealed, err := m.Keys.Seal(ctx, kernel.TenantOf(ctx), key, []byte(id.String()))
	if err != nil {
		return Pool{}, err
	}
	p, err := db.One[Pool](ctx, tx, `insert into serial_pool (id, tenant_id, product_id, gtin, market, mode, alphabet, length, prefix, key_material, capacity, low_watermark_pct)
		values ($1, app_tenant(), $2,$3,$4,$5,$6,$7,$8,$9,$10,$11) returning `+poolCols,
		id, prod.ID, prod.GTIN, in.Market, in.Mode, in.Alphabet, in.Length, in.Prefix, sealed, g.Capacity(), in.LowWatermarkPct)
	if err != nil {
		if errors.Is(err, db.ErrConflict) {
			return p, httpx.Conflict("a pool for this GTIN, market and prefix already exists")
		}
		return p, err
	}
	p.fill()
	m.gens.Store(id, g)
	_, err = audit.Write(ctx, tx, audit.Record{Action: "serialpool.create", Entity: "serial_pool", EntityID: id.String(), After: p})
	return p, err
}

func tweakFor(gtin, market string) []byte { return []byte(gtin + market) }

func (m *Module) createPool(w http.ResponseWriter, r *http.Request) error {
	var in PoolInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		p, err := m.CreatePool(r.Context(), tx, in)
		if err == nil {
			httpx.JSON(w, http.StatusCreated, p)
		}
		return err
	})
}

type poolKey struct {
	Pool
	TenantID    uuid.UUID `db:"tenant_id"`
	KeyMaterial []byte    `db:"key_material"`
}

// generator returns (and caches) the pool's generator.
func (m *Module) generator(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*Generator, Pool, error) {
	pk, err := db.One[poolKey](ctx, tx, `select `+poolCols+`, tenant_id, key_material from serial_pool where id=$1`, id)
	if err != nil {
		return nil, Pool{}, err
	}
	pk.fill()
	if g, ok := m.gens.Load(id); ok {
		return g.(*Generator), pk.Pool, nil
	}
	key, err := m.Keys.Open(ctx, pk.TenantID, pk.KeyMaterial, []byte(id.String()))
	if err != nil {
		return nil, pk.Pool, fmt.Errorf("unseal pool key: %w", err)
	}
	g, err := NewGenerator(pk.Mode, pk.Alphabet, pk.Length, pk.Prefix, key, tweakFor(pk.GTIN, pk.Market))
	if err != nil {
		return nil, pk.Pool, err
	}
	m.gens.Store(id, g)
	return g, pk.Pool, nil
}

// Allocate reserves count serials idempotently. Serial numbers are never reissued: the
// pool counter only moves forward, and replays with the same key return the same range.
func (m *Module) Allocate(ctx context.Context, poolID uuid.UUID, count int, idemKey string) (Allocation, error) {
	if count < 1 || count > MaxAllocation {
		return Allocation{}, httpx.BadRequest(fmt.Sprintf("quantity must be 1-%d", MaxAllocation))
	}
	if idemKey == "" || len(idemKey) > 200 {
		return Allocation{}, httpx.BadRequest("Idempotency-Key header is required (max 200 chars)")
	}
	p := auth.MustFrom(ctx)
	var a Allocation
	var g *Generator
	var lowAlert *Pool
	err := m.DB.Tx(ctx, p.TenantID, func(tx pgx.Tx) error {
		var err error
		var pool Pool
		g, pool, err = m.generator(ctx, tx, poolID)
		if err != nil {
			return err
		}
		if pool.Status != "active" {
			return httpx.Conflict("pool is " + pool.Status)
		}
		a.PoolID, a.GTIN = poolID, pool.GTIN
		var prevCount int
		err = tx.QueryRow(ctx, `select id, start_counter, count from serial_allocation where pool_id=$1 and idempotency_key=$2`, poolID, idemKey).Scan(&a.AllocationID, &a.Start, &prevCount)
		if err == nil {
			if prevCount != count {
				return httpx.Conflict("Idempotency-Key reused with a different quantity")
			}
			a.Count, a.Replayed = prevCount, true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var next, capacity int64
		var lowPct int
		err = tx.QueryRow(ctx, `update serial_pool set next_counter = next_counter + $2 where id=$1 and next_counter + $2 <= capacity
			returning next_counter, capacity, low_watermark_pct`, poolID, count).Scan(&next, &capacity, &lowPct)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.Conflict("pool exhausted: not enough serials remaining")
		}
		if err != nil {
			return err
		}
		a.AllocationID, a.Start, a.Count = ids.New(), next-int64(count), count
		if _, err := tx.Exec(ctx, `insert into serial_allocation (id, tenant_id, pool_id, idempotency_key, start_counter, count, requested_by) values ($1, app_tenant(), $2,$3,$4,$5,$6)`,
			a.AllocationID, poolID, idemKey, a.Start, count, p.Actor()); err != nil {
			return err
		}
		if _, err := audit.Write(ctx, tx, audit.Record{Action: "serial.allocate", Entity: "serial_pool", EntityID: poolID.String(),
			After: map[string]any{"allocationId": a.AllocationID, "start": a.Start, "count": count, "idempotencyKey": idemKey}}); err != nil {
			return err
		}
		remaining := capacity - next
		if lowWatermark(remaining, capacity, lowPct) {
			pool.Issued = next
			pool.fill()
			lowAlert = &pool
		}
		return nil
	})
	if err != nil {
		return a, err
	}
	if lowAlert != nil {
		m.raiseLowAlert(ctx, p.TenantID, *lowAlert)
	}
	a.Serials, err = g.Range(a.Start, a.Count)
	return a, err
}

func (m *Module) raiseLowAlert(ctx context.Context, tenant uuid.UUID, p Pool) {
	_ = m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into alert (id, tenant_id, kind, severity, title, detail, entity, entity_id, dedupe_key)
			values ($1, app_tenant(), 'serialization.pool_low', 'warning', $2, $3, 'serial_pool', $4, $5) on conflict do nothing`,
			ids.New(), fmt.Sprintf("Serial pool %s/%s is running low (%d left)", p.GTIN, p.Market, p.Remaining),
			map[string]any{"poolId": p.ID, "remaining": p.Remaining, "capacity": p.Capacity}, p.ID.String(), "pool_low:"+p.ID.String())
		return err
	})
	m.Bus.Publish(ctx, "serialization.pool_low", tenant, p.ID.String(), p)
}

func (m *Module) allocate(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Quantity int `json:"quantity"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	a, err := m.Allocate(r.Context(), id, in.Quantity, r.Header.Get("Idempotency-Key"))
	if err != nil {
		return err
	}
	status := http.StatusCreated
	if a.Replayed {
		status = http.StatusOK
	}
	httpx.JSON(w, status, a)
	return nil
}

// --- serial status checks used by EPCIS validation and verification ---

// SerialStatus is the lifecycle position of a serial within Open Spoor pools.
type SerialStatus string

// Serial statuses.
const (
	StatusUnmanaged   SerialStatus = "unmanaged" // no pool for this GTIN: not validated here
	StatusUnallocated SerialStatus = "unallocated"
	StatusAllocated   SerialStatus = "allocated"
	StatusCommitted   SerialStatus = "commissioned"
)

type poolRef struct {
	id      uuid.UUID
	gen     *Generator
	next    int64
	commish *roaring64.Bitmap
}

// Checker answers serial status questions for one tenant transaction.
type Checker struct {
	m     *Module
	tx    pgx.Tx
	pools map[string][]*poolRef // gtin -> pools
}

// NewChecker loads pools for the GTINs.
func (m *Module) NewChecker(ctx context.Context, tx pgx.Tx, gtins []string) (*Checker, error) {
	c := &Checker{m: m, tx: tx, pools: map[string][]*poolRef{}}
	if len(gtins) == 0 {
		return c, nil
	}
	rows, err := tx.Query(ctx, `select id, gtin, next_counter, commissioned from serial_pool where gtin = any($1)`, gtins)
	if err != nil {
		return nil, err
	}
	type row struct {
		id   uuid.UUID
		gtin string
		next int64
		bm   []byte
	}
	var rs []row
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.id, &x.gtin, &x.next, &x.bm); err != nil {
			rows.Close()
			return nil, err
		}
		rs = append(rs, x)
	}
	rows.Close()
	for _, x := range rs {
		g, _, err := m.generator(ctx, tx, x.id)
		if err != nil {
			return nil, err
		}
		bm := roaring64.New()
		if len(x.bm) > 0 {
			if err := bm.UnmarshalBinary(x.bm); err != nil {
				return nil, err
			}
		}
		c.pools[x.gtin] = append(c.pools[x.gtin], &poolRef{id: x.id, gen: g, next: x.next, commish: bm})
	}
	return c, nil
}

// Status returns the serial's status and its pool counter.
func (c *Checker) Status(gtin, serial string) (SerialStatus, *poolRef, uint64) {
	ps := c.pools[gtin]
	if len(ps) == 0 {
		return StatusUnmanaged, nil, 0
	}
	for _, p := range ps {
		n, err := p.gen.Counter(serial)
		if err != nil {
			continue
		}
		if n >= p.next {
			return StatusUnallocated, p, uint64(n)
		}
		if p.commish.Contains(uint64(n)) {
			return StatusCommitted, p, uint64(n)
		}
		return StatusAllocated, p, uint64(n)
	}
	return StatusUnallocated, nil, 0
}

// MarkCommissioned records commissioned counters and persists the bitmaps.
// It returns serials that were already commissioned (double commissioning).
func (c *Checker) MarkCommissioned(ctx context.Context, gtin string, serials []string) ([]string, error) {
	var dup []string
	touched := map[*poolRef]bool{}
	type res struct {
		st SerialStatus
		p  *poolRef
		n  uint64
	}
	rs := make([]res, len(serials))
	_ = epcislib.ParallelFor(len(serials), func(i int) error {
		st, p, n := c.Status(gtin, serials[i])
		rs[i] = res{st, p, n}
		return nil
	})
	for i, s := range serials {
		st, p, n := rs[i].st, rs[i].p, rs[i].n
		switch st {
		case StatusCommitted:
			dup = append(dup, s)
		case StatusAllocated:
			p.commish.Add(n)
			touched[p] = true
		}
	}
	for p := range touched {
		p.commish.RunOptimize()
		b, err := p.commish.MarshalBinary()
		if err != nil {
			return dup, err
		}
		if _, err := c.tx.Exec(ctx, `update serial_pool set commissioned=$2 where id=$1`, p.id, b); err != nil {
			return dup, err
		}
	}
	return dup, nil
}

// --- encode / decode ---

// CodeResult describes a pack identifier in every representation.
type CodeResult struct {
	GTIN          string `json:"gtin"`
	Serial        string `json:"serial,omitempty"`
	Lot           string `json:"lot,omitempty"`
	Expiry        string `json:"expiry,omitempty"`
	ElementString string `json:"elementString"`
	HRI           string `json:"hri"`
	DigitalLink   string `json:"digitalLink"`
	URN           string `json:"urn,omitempty"`
	Image         string `json:"image,omitempty"`
	ImageNote     string `json:"imageNote,omitempty"`
}

func (m *Module) describe(ctx context.Context, p gs1.Pack, withImage bool) (CodeResult, error) {
	cr := CodeResult{GTIN: p.GTIN, Serial: p.Serial, Lot: p.Lot, Expiry: p.Expiry,
		ElementString: strings.ReplaceAll(gs1.ElementString(p.AIs()), gs1.GS, "<GS>"), HRI: gs1.HRI(p.AIs()),
		DigitalLink: gs1.DigitalLink(m.Cfg.PublicURL, p)}
	cpLen := 7
	_ = m.Tx(ctx, func(tx pgx.Tx) error {
		if prod, err := masterdata.ByGTIN(ctx, tx, p.GTIN); err == nil {
			cpLen = prod.CompanyPrefixLength
		}
		return nil
	})
	if p.Serial != "" {
		cr.URN, _ = gs1.SGTINURN(p.GTIN, p.Serial, cpLen)
	}
	if withImage {
		// TODO(spec): GS1 DataMatrix requires FNC1 in the first codeword; the encoder used here
		// emits a plain ECC200 symbol of the element string. Print-ready GS1 symbols should come
		// from the line's Level 2 printer; this image is for previews and test labels.
		code, err := datamatrix.Encode(gs1.ElementString(p.AIs()))
		if err != nil {
			return cr, httpx.BadRequest("cannot encode DataMatrix: " + err.Error())
		}
		scaled, err := barcode.Scale(code, code.Bounds().Dx()*8, code.Bounds().Dy()*8)
		if err != nil {
			return cr, err
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, scaled); err != nil {
			return cr, err
		}
		cr.Image = "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
		cr.ImageNote = "preview symbol without FNC1; not for production printing"
	}
	return cr, nil
}

func (m *Module) encode(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		GTIN   string `json:"gtin"`
		Serial string `json:"serial"`
		Lot    string `json:"lot"`
		Expiry string `json:"expiry"`
		Format string `json:"format"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := gs1.Pack{GTIN: gs1.NormalizeGTIN(in.GTIN), Serial: in.Serial, Lot: in.Lot, Expiry: in.Expiry}
	if !gs1.ValidGTIN(p.GTIN) {
		return httpx.BadRequest("invalid GTIN")
	}
	if len(p.Serial) > 20 || len(p.Lot) > 20 {
		return httpx.BadRequest("serial and lot are limited to 20 characters")
	}
	if p.Expiry != "" {
		if _, err := gs1.ExpiryDate(p.Expiry); err != nil {
			return httpx.BadRequest("expiry must be YYMMDD")
		}
	}
	cr, err := m.describe(r.Context(), p, in.Format != "text")
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, cr)
	return nil
}

// ParseCode accepts a DataMatrix element string, HRI text, a GS1 Digital Link or an SGTIN URN.
func ParseCode(code string) (gs1.Pack, error) {
	code = strings.TrimSpace(code)
	code = strings.ReplaceAll(code, "<GS>", gs1.GS)
	code = strings.ReplaceAll(code, "{GS}", gs1.GS)
	var ais []gs1.AI
	var err error
	switch {
	case strings.HasPrefix(code, "urn:epc:id:sgtin:"):
		g, s, _, err := gs1.ParseSGTINURN(code)
		if err != nil {
			return gs1.Pack{}, err
		}
		return gs1.Pack{GTIN: g, Serial: s}, nil
	case strings.HasPrefix(code, "http://") || strings.HasPrefix(code, "https://"):
		ais, err = gs1.ParseDigitalLink(code)
	default:
		ais, err = gs1.ParseElementString(code)
	}
	if err != nil {
		return gs1.Pack{}, err
	}
	return gs1.PackFromAIs(ais)
}

func (m *Module) decode(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Code string `json:"code"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p, err := ParseCode(in.Code)
	if err != nil {
		return httpx.BadRequest("cannot decode: " + err.Error())
	}
	cr, err := m.describe(r.Context(), p, false)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, cr)
	return nil
}

// StatusMany evaluates many (gtin, serial) pairs in parallel.
func (c *Checker) StatusMany(gtins, serials []string) []SerialStatus {
	out := make([]SerialStatus, len(serials))
	_ = epcislib.ParallelFor(len(serials), func(i int) error {
		out[i], _, _ = c.Status(gtins[i], serials[i])
		return nil
	})
	return out
}

// lowWatermark reports remaining < capacity * pct / 100 without int64 overflow
// (FF1 pools can hold up to 2^62 serials).
func lowWatermark(remaining, capacity int64, pct int) bool {
	return float64(remaining) < float64(capacity)*float64(pct)/100
}
