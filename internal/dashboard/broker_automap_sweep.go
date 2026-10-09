package dashboard

import (
	"context"
	"fmt"
	"log"
	"time"
)

// autoMapSweepInterval 目录重识别周期（D-148）：商目录漂移/alias 表长进/归一规则修复
// 后，已 attach venue 的映射增量只在本循环与 UI 手动两条径补齐；4h 与 scan 轮同量级。
const autoMapSweepInterval = 4 * time.Hour

// RunAutoMapSweep 周期性目录重识别（D-148）：对全部 connected venue 逐个跑 autoMapVenue
// 内核（与 AutoMapBroker RPC 同语义幂等——已有映射跳过、只应用 auto 状态、
// conflict/multi_hit/trade_disabled 不自动应用）；新映射经既有
// ReloadSymbolMap+Resubscribe+swap 补轮径运行时生效，ticks/facts/universe 下游自动跟上。
// 业主域保留：映射增量记 info 告警留痕（D-147 口径：系统自决事件不推送）。
func (s *Service) RunAutoMapSweep(ctx context.Context) {
	t := time.NewTicker(autoMapSweepInterval)
	defer t.Stop()
	for {
		s.autoMapSweepOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// autoMapSweepOnce 单轮：connected venue 集来自 AllSymbolGroups（symbolGroups 就绪者）；
// 单 venue 失败只记日志不中断本轮（venue 断连/查询败下轮自愈）。
func (s *Service) autoMapSweepOnce(ctx context.Context) {
	groups, err := s.bm.AllSymbolGroups(ctx)
	if err != nil {
		log.Printf("[auto-map sweep] venues: %v", err)
		return
	}
	for venue := range groups {
		_, applied, skipped, err := s.autoMapVenue(ctx, venue)
		if err != nil {
			log.Printf("[auto-map sweep] %s: %v", venue, err)
			continue
		}
		if applied == 0 {
			continue
		}
		msg := fmt.Sprintf("[auto-map] %s 目录重识别 +%d 映射（skipped %d，自动订阅+facts 已生效）", venue, applied, skipped)
		if err := s.store.InsertAlert(ctx, nil, "info", msg); err != nil {
			log.Printf("[auto-map sweep] alert: %v", err)
		}
	}
}
