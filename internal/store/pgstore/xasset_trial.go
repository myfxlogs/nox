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

// XAssetGradNotePrefix 毕业链授权行 note 前缀（approve 径写 'D-403毕业 paper#N'，
// D-552 扩面——业主真金授权与 S2 委员会试单同级豁免属员）。
const XAssetGradNotePrefix = "D-403毕业"

// XAssetExemptNoteLikePatterns 豁免 note 前缀集（D-550+D-552 授权标集合：
// {'S2桥'=委员会试单,'D-403毕业'=业主真金授权}）——行级窄豁免判据其余四项
// （enabled/四元组/env/simulated）不变；集合外前缀不豁免（未授权行维持掐死）。
func XAssetExemptNoteLikePatterns() []string {
	return []string{XAssetTrialNotePrefix + "%", XAssetGradNotePrefix + "%"}
}

// IsTrialXAssetCombo 试单属员判：enabled ∧ note 前缀命中 ∧ (canonical 买/卖 +
// venue_key 买/卖) 四元组精确匹配 → true。environment 过滤同 LoadXAssetAllowCombos
// （simulated IS NULL 两域通用 / false=real / true=demo）。行缺=false 非错；
// 查询败→err（调用方 fail-closed 维持拒/掐流）。
func (s *Store) IsTrialXAssetCombo(ctx context.Context, canonicalBuy, canonicalSell, venueKeyBuy, venueKeySell, environment string) (bool, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		SELECT id FROM xasset_allow_combos
		WHERE enabled AND note LIKE ANY($1)
		  AND canonical_buy = $2 AND canonical_sell = $3
		  AND venue_key_buy = $4 AND venue_key_sell = $5
		  AND (simulated IS NULL OR simulated = ($6='demo'))
		LIMIT 1`,
		XAssetExemptNoteLikePatterns(), canonicalBuy, canonicalSell, venueKeyBuy, venueKeySell, environment).Scan(&id)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("trial combo query: %w", err)
	}
	return true, nil
}

// HasExemptXAssetCombo 豁免组合在册判（Stage-1 放行接线——checkAllStrategiesDisabled
// 豁免缝）：enabled∧note 前缀命中豁免集∧simulated=false 行存在 → true。
// 豁免集单源复用 XAssetExemptNoteLikePatterns（禁第二份字面量）。
func (s *Store) HasExemptXAssetCombo(ctx context.Context, environment string) (bool, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		SELECT id FROM xasset_allow_combos
		WHERE enabled AND note LIKE ANY($1)
		  AND (simulated IS NULL OR simulated = ($2='demo'))
		LIMIT 1`,
		XAssetExemptNoteLikePatterns(), environment).Scan(&id)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("exempt combo query: %w", err)
	}
	return true, nil
}
