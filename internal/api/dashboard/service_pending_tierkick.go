// service_pending_tierkick.go —— TierKickAll（棘轮拆出件，零行为变）。
package dashboard

import (
	"context"
	"fmt"
	"time"
)

// 安全→安全不发声（transition 语义，禁定时器）。
func (s *Service) TierKickAll(ctx context.Context) {
	if s == nil || s.store == nil {
		return // 部分构造 Service（D-471 测试字面量）nil 安全
	}
	combos, err := s.store.ListXAssetAllowCombosAll(ctx)
	if err != nil {
		return
	}
	snaps, err := s.liveAccountLatest(ctx)
	if err != nil {
		return
	}
	s.tierMu.Lock()
	defer s.tierMu.Unlock()
	if s.tierState == nil {
		s.tierState = map[string]tierState{} // R1：init 移入锁内（原锁外惰性 init=数据竞争）
	}
	now := time.Now()
	for _, c := range combos {
		if !c.Enabled {
			continue
		}
		tier, _, warn := s.comboTier(ctx, c, snaps)
		key := fmt.Sprintf("%d", c.ID)
		prev, seen := s.tierState[key]
		s.tierState[key] = tierState{tier: tier, at: now}
		// transition 语义：仅 安全(ok/unset)→超档(over) 转移发声；首见不发声（无前态）。
		if seen && prev.tier != "over" && tier == "over" && warn && now.Sub(prev.alertAt) >= tierAlertInterval {
			if err := s.store.InsertAlert(ctx, nil, "warn", fmt.Sprintf("[档位恶化] 组合#%d 超档：存续态档位由安全转超档，详见待入场区", c.ID)); err != nil {
				continue
			}
		}
		if warn {
			if st, ok2 := s.tierState[key]; ok2 {
				st.alertAt = now
				s.tierState[key] = st
			}
		}
	}
}

type tierState struct {
	tier    string
	at      time.Time
	alertAt time.Time
}
