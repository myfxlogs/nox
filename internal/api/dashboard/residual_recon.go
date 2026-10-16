// residual_recon.go —— D-539②：快照残差对账（写径同拍搭车，零新定时器）。
// 等式：equity_t = equity_{t-1} + Σ外部充提 + Σ划转净额 + Σ收益 + Σ损益。
// 口径边界（注释即口径，不臆造）：
//   - 范围=非保证金池（venue_type∈{spot,funding,earn}）——perp/unified 权益随未实现
//     盈亏逐帧漂移，无 UPL 台账对等式必噪（UPL 台账另票）；
//   - 币种=稳定币白名单（USDT/USDC/USD/FDUSD/BUSD/TUSD/DAI）——income_events 计 USD
//     额，对非稳定币仓行单位不一致不可比；
//   - 外部充提落点钱包按 venue 约定（binance→spot、okx→funding、gate→spot）——
//     venue 实际落点偏移会以残差告警暴露（自披露设计：台账漏项→unexplained 告警）；
//   - 残差阈=max(1 USD 尘埃口径(D-529), 0.5%×|equity|)；越阈→critical 24h 去重。
package dashboard

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"nox/internal/platform/safego"
	"nox/internal/store/pgstore"
)

// residReconVenueDepositWallet 外部充提落点钱包（venue→venue_type）——
// binance/gate 充提落 spot 主账户；okx 充提落 funding 资金账户（unified 为交易账户）。
var residReconVenueDepositWallet = map[string]string{
	"binance": "spot",
	"gate":    "spot",
	"okx":     "funding",
}

// residReconOrderSrcWallet 申购单扣款池（venue→EARN 活期源池/DCI 双币源池）：
// binance 两类申购皆扣 spot 主账户；okx EARN=savings purchase 只认 funding
// 户（D-549 装配实证），okx sfp 扣 unified 交易户=快照键域 'spot'
// （/account/balance 同账别名）；gate=现货户。
var residReconOrderSrcWallet = map[string][2]string{ // [0]=EARN 源池 [1]=DCI 源池
	"binance": {"spot", "spot"},
	"okx":     {"funding", "spot"},
	"gate":    {"spot", "spot"},
}

// residReconOrderDstEarn 申购入流落 earn 池快照的判定：binance 双币持仓计入
// earn 池（dciPositionsEarn 同源实证）、EARN 活期天然计入；okx sfp 结构化持仓
// 不入 savings 余额（earn 池口径）→ okx DCI 零 earn 腿。EARN 全 venue 计入。
func residReconOrderEarnDst(venue string, isDCI bool) bool {
	return !isDCI || venue == "binance"
}

var residReconPoolTypes = map[string]bool{"spot": true, "funding": true, "earn": true}
var residReconStableCcy = map[string]bool{
	"USDT": true, "USDC": true, "USD": true, "FDUSD": true, "BUSD": true, "TUSD": true, "DAI": true,
}

// residReconStore 残差对账读面+告警写口（*pgstore.Store 原生实现）。
type residReconStore interface {
	PrevSnapshotEquity(ctx context.Context, vn, venueType, ccy string, before time.Time) (float64, time.Time, bool, error)
	SumVenueIOExternal(ctx context.Context, venue, ccy string, from, to time.Time) (float64, error)
	SumVenueIOTransfers(ctx context.Context, venue, ccy, wallet string, from, to time.Time) (float64, error)
	SumIncomeForPool(ctx context.Context, venue, venueType, ccy string, from, to time.Time) (float64, error)
	// SumDCISettleWindow DCI 交割腿（本拍窗内 settled：outflow=本金离 earn 池、
	// inflow=本息到默认钱包——不可归因残差的自解释项）。
	SumDCISettleWindow(ctx context.Context, venue, ccy string, from, to time.Time) (outflow, inflow float64, err error)
	// SumDCIFilledWindow 申购腿（窗内翻 filled/settled：earnQty=EARN 申购额、
	// dciQty=双币申购额——源池−qty∧earn 池+qty 对称解释）。
	SumDCIFilledWindow(ctx context.Context, venue, ccy string, from, to time.Time) (earnQty, dciQty float64, err error)
	// SumDisposalProceedsWindow 处置回款腿（窗内 dci_exercise_disposal 损益行
	// join 原单 → 卖回得额，venue_type=回款落池）。
	SumDisposalProceedsWindow(ctx context.Context, venue, venueType string, from, to time.Time) (float64, error)
	InsertAlertDedup(ctx context.Context, level, message string, window time.Duration) error
}

// ResidualRecon 快照残差对账件（snapHook 搭车消费者；不阻塞写径——safego 异步）。
type ResidualRecon struct {
	store residReconStore
}

// NewResidualRecon 构造。
func NewResidualRecon(st residReconStore) *ResidualRecon { return &ResidualRecon{store: st} }

// OnSnapshots snapHook 入口：逐 (venue,venue_type,currency) 去重后异步评估。
func (r *ResidualRecon) OnSnapshots(ctx context.Context, snaps []pgstore.VenueAccountSnapshot) {
	if r.store == nil {
		return
	}
	seen := make(map[string]bool, len(snaps))
	for _, sn := range snaps {
		if !residReconPoolTypes[sn.VenueType] || !residReconStableCcy[strings.ToUpper(sn.Currency)] {
			continue // 保证金池/非稳定币=口径外（头注）
		}
		key := sn.Venue + "|" + sn.VenueType + "|" + sn.Currency
		if seen[key] {
			continue
		}
		seen[key] = true
		row := sn
		safego.GoPlain("resid_recon:"+key, func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := r.evaluate(ctx, &row); err != nil {
				log.Printf("[resid-recon] %s: %v", key, err)
			}
		})
	}
}

// evaluate 单池等式：Δequity − 外部流 − 划转净 − 收益 = 残差；越阈→critical 去重告警。
// 阈值=max($1 尘埃, 0.5%×|equity|)；首拍无 prev=基线化跳过（同 earn_yield 语义）。
func (r *ResidualRecon) evaluate(ctx context.Context, sn *pgstore.VenueAccountSnapshot) error {
	ccy := strings.ToUpper(sn.Currency)
	prev, prevTS, has, err := r.store.PrevSnapshotEquity(ctx, sn.Venue, sn.VenueType, sn.Currency, sn.FetchedAt)
	if err != nil {
		return fmt.Errorf("prev snapshot: %w", err)
	}
	if !has {
		return nil // 首拍基线化
	}
	var external float64
	if residReconVenueDepositWallet[sn.Venue] == sn.VenueType {
		external, err = r.store.SumVenueIOExternal(ctx, sn.Venue, ccy, prevTS, sn.FetchedAt)
		if err != nil {
			return fmt.Errorf("io external: %w", err)
		}
	}
	transfers, err := r.store.SumVenueIOTransfers(ctx, sn.Venue, ccy, sn.VenueType, prevTS, sn.FetchedAt)
	if err != nil {
		return fmt.Errorf("io transfers: %w", err)
	}
	income, err := r.store.SumIncomeForPool(ctx, sn.Venue, sn.VenueType, ccy, prevTS, sn.FetchedAt)
	if err != nil {
		return fmt.Errorf("income: %w", err)
	}
	// DCI 交割补腿：本拍窗内 settled 行——earn 池本金离场计入 outflow
	// （还本去向钱包端经 inflow 项对称解释，双侧同窗只对 settled_at 生效）。
	dciOut, dciIn, err := r.store.SumDCISettleWindow(ctx, sn.Venue, ccy, prevTS, sn.FetchedAt)
	if err != nil {
		return fmt.Errorf("dci settle window: %w", err)
	}
	// 申购腿：窗内翻 filled 的订单——本金源池−qty∧earn 桶+qty 对称解释
	// （源池/入池 venue 差分见 residReconOrderSrcWallet/residReconOrderEarnDst）。
	// status 域实查 {pending,filled,failed,rejected,settled} 已接全——pending/
	// failed/rejected 无资金动；EARN 活期赎回 venue 端无订单行属 io 台账域。
	earnQty, dciQty, err := r.store.SumDCIFilledWindow(ctx, sn.Venue, ccy, prevTS, sn.FetchedAt)
	if err != nil {
		return fmt.Errorf("dci filled window: %w", err)
	}
	src := residReconOrderSrcWallet[sn.Venue]
	lifecycle := 0.0
	if sn.VenueType == "earn" {
		lifecycle += earnQty - dciOut // 申购入池；本金离 earn 桶=正当下降
		if residReconOrderEarnDst(sn.Venue, true) {
			lifecycle += dciQty
		}
	}
	if residReconVenueDepositWallet[sn.Venue] == sn.VenueType {
		lifecycle += dciIn // 本息落到充提钱包
	}
	if src[0] == sn.VenueType {
		lifecycle -= earnQty // EARN 申购出源池
	}
	if src[1] == sn.VenueType {
		lifecycle -= dciQty // DCI 申购出源池
	}
	// 处置回款：接货卖回得额=USDT，落 venue 记 venue_type 池（binance spot；
	// okx 交易户在快照键域别名 spot——venue_normalize 同键实证）。
	if ccy == "USDT" {
		proceeds, derr := r.store.SumDisposalProceedsWindow(ctx, sn.Venue, sn.VenueType, prevTS, sn.FetchedAt)
		if derr != nil {
			return fmt.Errorf("disposal proceeds window: %w", derr)
		}
		lifecycle += proceeds
	}
	delta := sn.EquityRaw - prev
	residual := delta - external - transfers - income - lifecycle
	thr := 1.0
	if t := 0.005 * absF(sn.EquityRaw); t > thr {
		thr = t
	}
	if absF(residual) <= thr {
		return nil
	}
	return r.store.InsertAlertDedup(ctx, "critical", fmt.Sprintf(
		"[残差对账] %s:%s %s 权益变动无台账解释: prev=%.4f → %.4f (Δ%.4f), 外部流%.4f 划转%.4f 收益%.4f 订单腿%.4f, 未解释残差%.4f (阈%.4f) —— 查 io/划转/收益/订单账缺项",
		sn.Venue, sn.VenueType, ccy, prev, sn.EquityRaw, delta, external, transfers, income, lifecycle, residual, thr), 24*time.Hour)
}

func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
