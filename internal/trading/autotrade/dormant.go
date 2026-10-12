// dormant.go —— D-209：XASSET_CARRY 休眠轨消费点（D-204 DormantPairs 复用，禁复写）。
package autotrade

import (
	"context"
	"fmt"
	"log"

	"nox/internal/store/pgstore"
)

// xassetDormantSkip canonical×venue 粒度休眠判——键形 xasset_carry:XAU@okx（非 venue 对：
// XAU@okx 休眠不误伤 XAG@okx / XAU@gate）。双腿 venue 各查一次。
// 命中→info 台账行（零推送）+skip；约束读败→fail-closed 同样 skip（与 D-204 休眠=降速语义不同，
// carry 执行径休眠判按风控闸 fail-closed）。
func (e *Engine) xassetDormantSkip(ctx context.Context, opp *pgstore.Opportunity) bool {
	cons, err := e.store.ListCapabilityConstraints(ctx)
	if err != nil {
		log.Printf("[autotrade] skip %s: 休眠约束读败 fail-closed: %v", opp.ID, err)
		return true
	}
	key, hit := pgstore.XAssetDormantHit(cons, opp.Canonical, opp.VenueBuy, opp.VenueSell)
	if !hit {
		return false
	}
	log.Printf("[autotrade] skip %s: 休眠轨 %s", opp.ID, key)
	if err := e.store.InsertAlert(ctx, nil, "info", fmt.Sprintf(
		"休眠轨（D-209）%s 跳过提案（opp %s）", key, opp.ID)); err != nil {
		log.Printf("[autotrade] dormant alert %s: %v", key, err)
	}
	return true
}
