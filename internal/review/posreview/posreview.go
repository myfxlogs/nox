// Package posreview —— D-266 M4.5：复盘员事件驱动判决书。
//
// posChange kick → 查「CLOSED ∧ 无 position_reviews」缺口集 → 逐仓产判决书
// （position_id PK 幂等 upsert）。boot 首扫补齐存量 backlog。
// 数字面纯 Go 零 LLM：expected=expected_net_bps×notional/1e4，actual=settlement
// .net_realized_usd（pnl_quality≠actual→NULL+质量痕）；exit_kind 权威枚举直读。
// LLM 职责只到归因叙事 review_note+followup_action（schema 校验 fail-visible，
// 建议不落 fin_strategy——淘汰权归判据表）。
package posreview

import (
	"context"
	"log"
	"time"

	"nox/internal/platform/analyst"
	"nox/internal/store/pgstore"
)

// Deps 复盘员依赖面（生产=*pgstore.Store+ResolveClient+InsertAlertDedup）。
type Deps struct {
	Store   Store
	Now     func() time.Time                                   // 测试注入；零值=time.Now
	Resolve func(ctx context.Context) (*analyst.Client, error) // AI 未配→nil+err→只落 Go 面
	Run     func(ctx context.Context, c *analyst.Client, msgs []analyst.Message, system string, cb analyst.LoopCallbacks) (analyst.LoopResult, error)
	Beat    func() // 名册心跳（装配层挂 safego.Beat）
}

// Store 复盘员读写面。
type Store interface {
	ListClosedWithoutReview(ctx context.Context) ([]pgstore.ClosedForReviewRow, error)
	GetSettlementByPos(ctx context.Context, posID int64) (*pgstore.SettlementRow, error)
	UpsertPositionReview(ctx context.Context, r *pgstore.PositionReview) error
	InsertAlertDedup(ctx context.Context, level, message string, window time.Duration) error
}

func (d *Deps) now() time.Time {
	if d.Now == nil {
		return time.Now()
	}
	return d.Now()
}

func (d *Deps) fill() {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Run == nil {
		d.Run = func(ctx context.Context, c *analyst.Client, msgs []analyst.Message, system string, cb analyst.LoopCallbacks) (analyst.LoopResult, error) {
			return analyst.RunWithSpecs(ctx, c, nil, nil, system, msgs, cb)
		}
	}
}

// Run 事件循环：boot 扫存量 + kick 逐次扫缺口（事件驱动零定时器）。
// kick=posChange 订阅 channel（每次 positions 写提交即发——全平仓径覆盖）。
func Run(ctx context.Context, d Deps, kicks <-chan struct{}) {
	d.fill()
	d.Sweep(ctx) // boot 补存量（D-266：SG 12 行 backlog 首班补齐）
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-kicks:
			if !ok {
				return
			}
			d.Sweep(ctx)
		}
	}
}

// Sweep 单拍：缺口集逐仓产判决书（单仓失败 fail-open 续跑，批次容错）。
func (d *Deps) Sweep(ctx context.Context) {
	rows, err := d.Store.ListClosedWithoutReview(ctx)
	if err != nil {
		log.Printf("[posreview] 缺口集读败: %v", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	if d.Beat != nil {
		d.Beat()
	}
	client, rerr := d.client(ctx)
	for _, row := range rows {
		if ctx.Err() != nil {
			return
		}
		if err := d.ReviewOne(ctx, client, row); err != nil {
			log.Printf("[posreview] pos#%d 判决书败: %v", row.ID, err)
		}
	}
	if rerr != nil {
		log.Printf("[posreview] AI 未配——本拍 %d 行只落 Go 数字面（note 标无叙事）", len(rows))
	}
}

func (d *Deps) client(ctx context.Context) (*analyst.Client, error) {
	if d.Resolve == nil {
		return nil, errNilResolve
	}
	return d.Resolve(ctx)
}
