package pgstore

// xasset_trial.go —— D-550：S2 桥试单行标记与豁免判。试单=委员会级独立授权
// 挂组合行（宿主策略退役冻结其存量组合域，不溯及后批试单）——豁免判只认
// 桥写组合行（note 前缀标），消费面=检测侧掐流/gate 策略拒/白名单 spec Trial 位。
import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// XAssetTrialNotePrefix S2 桥写组合行 note 前缀（s2bridge 写侧与三本读侧单源）。
const XAssetTrialNotePrefix = "S2桥"

// IsTrialXAssetCombo 试单属员判：enabled ∧ note 前缀命中 ∧ (canonical 买/卖 +
// venue_key 买/卖) 四元组精确匹配 → true。environment 过滤同 LoadXAssetAllowCombos
// （simulated IS NULL 两域通用 / false=real / true=demo）。行缺=false 非错；
// 查询败→err（调用方 fail-closed 维持拒/掐流）。
func (s *Store) IsTrialXAssetCombo(ctx context.Context, canonicalBuy, canonicalSell, venueKeyBuy, venueKeySell, environment string) (bool, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		SELECT id FROM xasset_allow_combos
		WHERE enabled AND note LIKE $1
		  AND canonical_buy = $2 AND canonical_sell = $3
		  AND venue_key_buy = $4 AND venue_key_sell = $5
		  AND (simulated IS NULL OR simulated = ($6='demo'))
		LIMIT 1`,
		XAssetTrialNotePrefix+"%", canonicalBuy, canonicalSell, venueKeyBuy, venueKeySell, environment).Scan(&id)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("trial combo query: %w", err)
	}
	return true, nil
}
