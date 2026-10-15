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

	"nox/internal/data/bus"
	"nox/internal/data/keydomain"
	"nox/internal/ledger/reconcile"
	"nox/internal/store/pgstore"
)

// Src 资金来源：ws=私有流 / rest=实拉校准 / mt_frame=MT5 流帧 / snap=快照兜底。
const (
	SrcWS      = "ws"
	SrcRest    = "rest"
	SrcMTFrame = "mt_frame"
	SrcSnap    = "snap"
)

// Funds 统一资金读口返回值。Live=true 仅当 Src∈{ws,rest,mt_frame}（实拉源）；
// snap 行 Note 必带龄注记（禁静默）。
type Funds struct {
	VenueKey     string  // 实际取到的池键（unified 回退时如实反映口径）
	Available    float64 // 可用资金（live=venue 实采可用；snap=free_margin_raw）
	HasAvailable bool    // false → 调用方 equity 回退（同 allocUsable 语义）
	Balance      float64 // 余额（仅快照行有值；live 源 HasBalance=false）
	HasBalance   bool
	Equity       float64
	Margin       float64 // D-127：占用保证金（仅 MT 帧直带>0；CEX/快照=0，调用方 eq−Available 推）
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
	// SnapWrite D-523①：REST 校准落快照写口（装配=st.InsertVenueAccountSnapshots；
	// nil=只回写活账本不落库）。值变跨阈才写，同值零写（calibBase 内存基准）。
	SnapWrite func(ctx context.Context, snaps []pgstore.VenueAccountSnapshot) error
	// MTFrames D-572 S2：MT 键先走 AccountMonitor.StreamAccount 帧缓存（装配处包装，
	// 零新依赖方向）；nil=帧源未接线→快照直通。帧龄窗=MTFrameTTL（快照龄闸同值注入，
	// 单一常量两处消费）。
	MTFrames   func(venueKey string) (MTFrame, bool)
	MTFrameTTL func() time.Duration
}

// MTFrame MT5 实时账户帧读口返回值（ObserveStreamAccount 落缓存数据面）。
type MTFrame struct {
	Equity    float64
	Available float64 // free margin
	Margin    float64 // D-127：占用保证金（帧直带>0 优先于 eq−fm 推导）
	Currency  string
	At        time.Time // 收帧时刻
}

// DefaultRestTTL REST 校准缓存期缺省（同 D-542 口径 30s）。
const DefaultRestTTL = 30 * time.Second

// View 统一读口（全消费点共享一实例——mu=REST 校准单飞）。
type View struct {
	d         Deps
	mu        sync.Mutex // REST 校准单飞
	calibMu   sync.Mutex // D-523①：快照去重基准独立锁（mu 持锁段内不可再取）
	calibBase map[string]pgstore.VenueAccountSnapshot
}

func New(d Deps) *View { return &View{d: d} }

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
	return v.live(ctx, venueKey, nil, false)
}

// LiveFresh D-563 S4c：决策面读口——ws 实推仍直读，REST 校准值不享 restTTL
// 复用（TTL 内 rest 条目对开仓判定=变相快照）→ 强制 REST 实拉；展示面走 Live/LiveSnaps。
func (v *View) LiveFresh(ctx context.Context, venueKey string) (Funds, bool) {
	return v.live(ctx, venueKey, nil, true)
}

// LiveSnaps 同 Live，快照兜底用调用方预取面（批量消费点省每行一查）。
func (v *View) LiveSnaps(ctx context.Context, venueKey string, snaps []pgstore.VenueAccountLatest) (Funds, bool) {
	return v.live(ctx, venueKey, snaps, false)
}

// calibrate 脏/空路径：单飞互斥下一发 REST 校准（复用 CEXAccountQuerier 经代理）→
// 回写账本 src=rest（RestTTL 内复用）。败→false 走快照兜底。
// exact=true（D-130 域1 展示面）：账本复查限精确键，PoolKeys 回退不复用（错账根因）。
func (v *View) calibrate(ctx context.Context, venueKey string, force, exact bool) (Funds, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	// 获锁后复查——并发腿可能刚校准过同池（spot×perp 两腿同 venue 常见）。
	// force=决策面实拉跳过复用（TTL 缓存对决策=变相快照）。
	k := keydomain.Parse(venueKey)
	if v.d.Ledger != nil {
		recheckKeys := PoolKeys(k.Venue, k.Type)
		if exact {
			recheckKeys = []string{keydomain.Join(k.Venue, k.Type)}
		}
		for _, key := range recheckKeys {
			if e, ok := v.d.Ledger.Get(key, v.restTTL(), time.Now()); ok {
				if !force || e.Src != SrcRest {
					return liveFunds(e), true
				}
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
	// 快照行写本币真值：cex 分币池 acct.Equity=Σdetails[*].eqUsd（全币 USD 总权益，
	// 保证金口径用）挂 acct.Currency 单币标签即伪账（okx unified 实证：rest_calib
	// 写 43.5 vs okx_event 写 availBal 3.4e-9——同键双写致 NAV 池 flap ±43.5）。
	// 资金可用性 Ledger/liveFunds 语义零动，仅校准快照行收窄到本币切片。
	calibAvail, calibEq := avail, acct.Equity
	if acct.AssetType == "cex" {
		calibAvail = acct.FreeQtyByCcy[acct.Currency] // 未填→0 诚实（与 rest_poll 分币行同径）
		calibEq = calibAvail
		for _, h := range acct.Holdings {
			if h.Ccy == acct.Currency {
				calibEq = h.UsdtValue // 本币权益切片（venue 已供 eqUsd；USDT 1:1 同值）
				break
			}
		}
	}
	v.writeCalibSnapshot(ctx, poolKey, calibAvail, calibEq, acct.Currency, at, acct.Simulated) // D-523①
	return liveFunds(bus.BalanceEntry{
		VenueKey: poolKey, Available: avail, Equity: acct.Equity,
		Currency: acct.Currency, At: at, Src: SrcRest,
	}), true
}

// mtFrame D-572 S2：MT 键流帧读口——帧命中且龄≤MTFrameTTL（快照龄闸同值注入）→
// live 读数（Src=mt_frame，At=收帧时刻）；miss/超窗/未装配→false 回落快照直通。
func (v *View) mtFrame(venueKey string) (Funds, bool) {
	if v.d.MTFrames == nil {
		return Funds{}, false
	}
	fr, ok := v.d.MTFrames(venueKey)
	if !ok {
		return Funds{}, false
	}
	if v.d.MTFrameTTL == nil { // 龄窗未注入=装配不完整→帧源禁用（禁另立字面量，不猜窗）
		return Funds{}, false
	}
	if time.Since(fr.At) > v.d.MTFrameTTL() {
		return Funds{}, false
	}
	return Funds{
		VenueKey: venueKey, Available: fr.Available, HasAvailable: fr.Available > 0,
		Equity: fr.Equity, Margin: fr.Margin, Currency: fr.Currency, At: fr.At,
		Src: SrcMTFrame, Live: true,
	}, true
}

func liveFunds(e bus.BalanceEntry) Funds {
	return Funds{
		VenueKey: e.VenueKey, Available: e.Available, HasAvailable: true,
		Equity: e.Equity, Currency: e.Currency, At: e.At,
		Src: e.Src, Live: true,
	}
}
