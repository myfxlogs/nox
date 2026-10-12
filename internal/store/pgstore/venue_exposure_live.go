// venue_exposure_live.go D-009 S4：venue 系统实占——在途仓（OPENING/OPEN/LEGGED/CLOSING）
// 的 OPEN 腿真表行（positions/position_legs；外单不入——非系统行本不在表内）。
// 行级 USD 定价（qty×cs×现价×fx）在 dashboard 侧完成（需 qbus/cs/fx 解析链），
// 本层只返回原子行；展示闸（闸6）与建库守卫（CreatePositionWithCapGuard）共用本查询（P3）。
package pgstore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// OpenVenueLegRow venue 实占原子行：一行=该 venue 名下一条未 FAILED 的 OPEN 腿。
// Pos* 列=腿所属持仓上下文（同窗兄弟仓在途无腿时由 pos-only 行承托——见 sibling 分支）。
type OpenVenueLegRow struct {
	PositionID   int64
	PosNotional  *float64 // positions.notional_usd（系统建库名义）
	PosOppID     string   // positions.opportunity_id（'TYPE:canon:venueA:vtA:venueB:vtB[:id]'）
	PosLegsTotal int      // 该 position OPEN 腿总数（0=同窗兄弟在途、腿未落库）
	VenueKey     string   // position_legs.venue_key（typed，本 venue 腿行才非空）
	Symbol       string
	Side         string
	Qty          float64
	PriceActual  *float64
	Status       string
}

// venueLegQuerier pool/tx 共用查询面（P3：守卫 tx 内复查与展示路径同一 SQL）。
type venueLegQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// openVenueSystemLegRows 在途仓腿行+无腿持仓行（LEFT JOIN：venue 腿缺席的行
// symbol 为空——PosLegsTotal=0 时该仓为同窗在途，需 oppID venue 归属判断）。
func openVenueSystemLegRows(ctx context.Context, q venueLegQuerier, venueBare string) ([]OpenVenueLegRow, error) {
	rows, err := q.Query(ctx, `
		SELECT p.id, p.notional_usd, COALESCE(p.opportunity_id,''),
		       l.venue_key, l.symbol, l.side, l.qty, l.price_actual, l.status,
		       (SELECT count(*) FROM position_legs x
		         WHERE x.position_id=p.id AND x.phase='OPEN')
		FROM positions p
		LEFT JOIN position_legs l
		  ON l.position_id=p.id AND l.phase='OPEN'
		 AND split_part(l.venue_key,':',1)=$1 AND l.status<>'FAILED'
		WHERE p.status IN ('OPENING','OPEN','LEGGED','CLOSING')
		  AND (
			EXISTS (
				SELECT 1 FROM position_legs e
				WHERE e.position_id=p.id AND e.phase='OPEN'
				  AND split_part(e.venue_key,':',1)=$1 AND e.status<>'FAILED'
			)
			OR NOT EXISTS (
				SELECT 1 FROM position_legs e
				WHERE e.position_id=p.id AND e.phase='OPEN'
			)
		  )
		ORDER BY p.id`, venueBare)
	if err != nil {
		return nil, fmt.Errorf("open venue system legs %s: %w", venueBare, err)
	}
	defer rows.Close()
	var out []OpenVenueLegRow
	for rows.Next() {
		var r OpenVenueLegRow
		var vk, sym, side, status *string
		var qty *float64
		if err := rows.Scan(&r.PositionID, &r.PosNotional, &r.PosOppID,
			&vk, &sym, &side, &qty, &r.PriceActual, &status, &r.PosLegsTotal); err != nil {
			return nil, fmt.Errorf("scan venue system leg %s: %w", venueBare, err)
		}
		if vk != nil && *vk != "" {
			r.VenueKey, r.Symbol, r.Side, r.Status = *vk, strOr(sym), strOr(side), strOr(status)
			if qty != nil {
				r.Qty = *qty
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func strOr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// OpenVenueSystemLegs 非事务版（展示闸/告警路径）。
func (s *Store) OpenVenueSystemLegs(ctx context.Context, venueBare string) ([]OpenVenueLegRow, error) {
	return openVenueSystemLegRows(ctx, s.pool, venueBare)
}
