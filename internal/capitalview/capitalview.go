// capitalview —— D-549 S1：全项目资金判定路径统一读口。
// 形态=D-542 活账本（私有流主源 bus.BalanceLedger）→ 脏/空一发 REST 校准回写 →
// REST 败快照兜底（带龄注记，禁静默）。一切资金判定点只经 Live 取资金数；
// 快照表降级=历史账+最后兜底（S3）——「快照+墙钟窗」作主判定的路径根除。
// 执行面（autotrade gate_margin 等开仓闸）：Src=="snap" 不可用——fail-closed
// （不降级用陈旧快照开真金仓，D-549 §5 安全裁定）。
package capitalview

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"arb/internal/bus"
	"arb/internal/keydomain"
	"arb/internal/reconcile"
	"arb/internal/store/pgstore"
)

// Src 资金来源：ws=私有流 / rest=实拉校准 / snap=快照兜底。
const (
	SrcWS   = "ws"
	SrcRest = "rest"
	SrcSnap = "snap"
)

// Funds 统一资金读口返回值。Live=true 仅当 Src∈{ws,rest}（实拉源）；
// snap 行 Note 必带龄注记（禁静默）。
type Funds struct {
	VenueKey     string  // 实际取到的池键（unified 回退时如实反映口径）
	Available    float64 // 可用资金（live=venue 实采可用；snap=free_margin_raw）
	HasAvailable bool    // false → 调用方 equity 回退（同 allocUsable 语义）
	Balance      float64 // 余额（仅快照行有值；live 源 HasBalance=false）
	HasBalance   bool
	Equity       float64
	Currency     string
	At           time.Time
	Src          string // "ws" | "rest" | "snap"
	Live         bool   // ws/rest 实拉源
	Note         string // snap 兜底龄注记等
}

// Deps 装配参数（dashboard 注入：活账本 + CEXAccountQuerier 解析器 + 快照兜底读口）。
type Deps struct {
	Ledger     *bus.BalanceLedger   // nil=活账本未装配→直走 REST 校准/快照兜底
	RestTTL    func() time.Duration // 校准缓存期（动态求值，测试可调）；nil=DefaultRestTTL
	QuerierFor func(ctx context.Context, venueKey string) (reconcile.AccountQuerier, string, bool)
	Snapshots  func(ctx context.Context) ([]pgstore.VenueAccountLatest, error)
}

// DefaultRestTTL REST 校准缓存期缺省（同 D-542 口径 30s）。
const DefaultRestTTL = 30 * time.Second

// View 统一读口（全消费点共享一实例——mu=REST 校准单飞）。
type View struct {
	d  Deps
	mu sync.Mutex
}

func New(d Deps) *View { return &View{d: d} }

func (v *View) restTTL() time.Duration {
	if v.d.RestTTL != nil {
		return v.d.RestTTL()
	}
	return DefaultRestTTL
}

// IsCEXVenue 腿 venue 是否 CEX（活账本通道覆盖三所；MT venue 段=数字账户号不走此径）。
func IsCEXVenue(venueKey string) bool {
	switch keydomain.Parse(venueKey).Venue {
	case "binance", "okx", "gate":
		return true
	}
	return false
}

// PoolKeys 活账本候选键序（与快照回退链同构：精确→unified→perp）。
// venueKey 撞键 #id 后缀剥除——账本键=裸 canonical 池键（账户级口径）。
func PoolKeys(bare, legType string) []string {
	if i := strings.IndexByte(legType, '#'); i >= 0 {
		legType = legType[:i]
	}
	out := make([]string, 0, 3)
	for _, t := range []string{legType, "unified", "perp"} {
		if t == "" {
			continue
		}
		k := keydomain.Join(bare, t)
		dup := false
		for _, o := range out {
			if o == k {
				dup = true
			}
		}
		if !dup {
			out = append(out, k)
		}
	}
	return out
}

// SnapPick 快照池行（与 D-358fix allocPoolOf 同序：精确 venue_type→unified→perp→
// 该 venue 任意非空 equity 行）。CEX/MT 同构——MT 快照即其采集数据面。
func SnapPick(snaps []pgstore.VenueAccountLatest, venueKey string) (pgstore.VenueAccountLatest, bool) {
	k := keydomain.Parse(venueKey)
	bare, vt := k.Venue, k.Type
	pick := func(wantType string) (pgstore.VenueAccountLatest, bool) {
		for _, r := range snaps {
			if r.Venue == bare && r.VenueType == wantType && r.EquityRaw != nil {
				return r, true
			}
		}
		return pgstore.VenueAccountLatest{}, false
	}
	for _, t := range []string{vt, "unified", "perp"} {
		if r, ok := pick(t); ok {
			return r, true
		}
	}
	for _, r := range snaps {
		if r.Venue == bare && r.EquityRaw != nil {
			return r, true
		}
	}
	return pgstore.VenueAccountLatest{}, false
}

// SnapFundsOf 快照行→Funds（兜底层级如实标 Src=snap+龄注记）。
func SnapFundsOf(r pgstore.VenueAccountLatest, now time.Time) Funds {
	f := Funds{
		VenueKey: keydomain.Join(r.Venue, r.VenueType),
		Equity:   *r.EquityRaw,
		Currency: r.Currency,
		At:       r.FetchedAt,
		Src:      SrcSnap,
		Note:     fmt.Sprintf("快照兜底（%s 前采集）", now.Sub(r.FetchedAt).Round(time.Second)),
	}
	if r.FreeMarginRaw != nil {
		f.Available, f.HasAvailable = *r.FreeMarginRaw, true
	}
	if r.BalanceRaw != nil {
		f.Balance, f.HasBalance = *r.BalanceRaw, true
	}
	return f
}

// Live 统一读口：CEX→活账本直读（干净零 REST）→脏/空一发 REST 校准回写→
// REST 败快照兜底（带龄注记）；MT/未装配→快照直通（MT 快照即其数据面）。
func (v *View) Live(ctx context.Context, venueKey string) (Funds, bool) {
	return v.live(ctx, venueKey, nil)
}

// LiveSnaps 同 Live，快照兜底用调用方预取面（批量消费点省每行一查）。
func (v *View) LiveSnaps(ctx context.Context, venueKey string, snaps []pgstore.VenueAccountLatest) (Funds, bool) {
	return v.live(ctx, venueKey, snaps)
}

func (v *View) live(ctx context.Context, venueKey string, snaps []pgstore.VenueAccountLatest) (Funds, bool) {
	if IsCEXVenue(venueKey) {
		k := keydomain.Parse(venueKey)
		if v.d.Ledger != nil {
			for _, key := range PoolKeys(k.Venue, k.Type) {
				if e, ok := v.d.Ledger.Get(key, v.restTTL(), time.Now()); ok {
					return liveFunds(e), true
				}
			}
		}
		// 账本未装配也走 REST 校准（执行面裁定：REST 败才 fail-closed，不因账本缺席直落快照）。
		if f, ok := v.calibrate(ctx, venueKey); ok {
			return f, true
		}
	}
	if snaps == nil {
		if v.d.Snapshots == nil {
			return Funds{}, false
		}
		var err error
		if snaps, err = v.d.Snapshots(ctx); err != nil {
			return Funds{}, false
		}
	}
	r, ok := SnapPick(snaps, venueKey)
	if !ok {
		return Funds{}, false
	}
	return SnapFundsOf(r, time.Now()), true
}

// calibrate 脏/空路径：单飞互斥下一发 REST 校准（复用 CEXAccountQuerier 经代理）→
// 回写账本 src=rest（RestTTL 内复用）。败→false 走快照兜底。
func (v *View) calibrate(ctx context.Context, venueKey string) (Funds, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	// 获锁后复查——并发腿可能刚校准过同池（spot×perp 两腿同 venue 常见）。
	k := keydomain.Parse(venueKey)
	if v.d.Ledger != nil {
		for _, key := range PoolKeys(k.Venue, k.Type) {
			if e, ok := v.d.Ledger.Get(key, v.restTTL(), time.Now()); ok {
				return liveFunds(e), true
			}
		}
	}
	if v.d.QuerierFor == nil {
		return Funds{}, false
	}
	q, poolKey, ok := v.d.QuerierFor(ctx, venueKey)
	if !ok {
		return Funds{}, false
	}
	acct, err := q.Account(ctx)
	if err != nil {
		log.Printf("[capitalview] %s REST 校准失败: %v（快照兜底）", venueKey, err)
		return Funds{}, false
	}
	avail := acct.FreeMargin
	if !(avail > 0) {
		avail = acct.Equity // venue 未供可用口径→净值回退（与 allocUsable 同语义）
	}
	at := time.Now()
	if v.d.Ledger != nil {
		v.d.Ledger.Put(poolKey, avail, acct.Equity, acct.Currency, SrcRest, at)
	}
	return liveFunds(bus.BalanceEntry{
		VenueKey: poolKey, Available: avail, Equity: acct.Equity,
		Currency: acct.Currency, At: at, Src: SrcRest,
	}), true
}

func liveFunds(e bus.BalanceEntry) Funds {
	return Funds{
		VenueKey: e.VenueKey, Available: e.Available, HasAvailable: true,
		Equity: e.Equity, Currency: e.Currency, At: e.At,
		Src: e.Src, Live: true,
	}
}
