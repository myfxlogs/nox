// position_strategy_key.go —— D-577 仓位归组键解析（写径单点，复审退回补正②：
// 粒度病灶修）。provenance 优先：opportunity→xasset_allow_combos（enabled∧
// 'D-403毕业 paper#N' note 精确四元组——毕业链仓位唯一权威策略定义面）→
// paper_spot_carry.structure→判据表 structure 键；无 provenance→engine_type
// 回落键（旧引擎名域内唯一定值才可信）。全解不开→NULL（读面 unassigned）。
package pgstore

import (
	"context"
	"errors"
	"log"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// queryRower pool/tx 通用面（CreatePosition 与 createPositionInTx 双写径同源）。
type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// resolvePositionStrategyKey 归组键解析（判据表驱动，禁硬编码映射）。
// ①provenance：opportunity 行（canonical+venue 对）精确匹配 enabled 毕业 combo
//
//	（note 'D-403毕业 paper#N' 唯一权威链）→paper.structure→map('structure',v)；
//
// ②回落：map('engine_type',p.Type)；③NULL=未归组（read 面 unassigned=true）。
// 判据表/provenance 读败≠行缺：ErrNoRows→NULL 照走；真 DB 错→warn 留痕不阻塞
// 开仓（复审③：混「无行」与真错于 NULL=静默不可）。
func resolvePositionStrategyKey(ctx context.Context, qr queryRower, opportunityID, engineType string) *string {
	key := func(kind, val string) *string {
		var k string
		err := qr.QueryRow(ctx,
			`SELECT strategy_key FROM fin_position_strategy_map WHERE source_kind=$1 AND source_val=$2`,
			kind, val).Scan(&k)
		switch {
		case err == nil:
			return &k
		case errors.Is(err, pgx.ErrNoRows):
			return nil
		default:
			log.Printf("[pos-group] strategy_map %s=%s 读败: %v", kind, val, err)
			return nil
		}
	}
	// ①provenance 精确键（structure）——多 combo 命中取首（组合四元组唯一约束保证至多一行）。
	var structure string
	err := qr.QueryRow(ctx, `
		SELECT p.structure FROM opportunities o
		JOIN xasset_allow_combos c ON c.enabled
		  AND o.canonical IN (c.canonical_buy, c.canonical_sell)
		  AND c.venue_buy = o.venue_buy AND c.venue_sell = o.venue_sell
		  AND c.note LIKE 'D-403毕业 paper#%'
		JOIN paper_spot_carry p ON p.id = CAST(substring(c.note from 'paper#(\d+)') AS bigint)
		WHERE o.id = $1 LIMIT 1`, opportunityID).Scan(&structure)
	switch {
	case err == nil:
		if k := key("structure", structure); k != nil {
			return k
		}
		// provenance 解出结构但 map 无该 structure 行 → 回落 engine_type（如实）。
	case errors.Is(err, pgx.ErrNoRows):
		// 无 provenance 链（非毕业仓/非 xasset）→回落。
	default:
		log.Printf("[pos-group] provenance 解析 opp=%s 读败: %v", opportunityID, err)
	}
	return key("engine_type", engineType)
}

var _ queryRower = (*pgxpool.Pool)(nil) // 接口合规断言（tx 端 pgx.Tx 同签名）
