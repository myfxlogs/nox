// legged_defer.go —— D-175：混合腿 LEGGED 裸露告警延迟判定的登记态。
// 键=position_id；态存 app_config JSON 单键（alert_episodes 先例，零 schema）。
// finalize 侧只登记不发射；reconcile.Run 惰性复查：CLOSED→降级 info，
// 逾期仍 LEGGED/FAILED→critical「需人工介入」。
package pgstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"
)

const cfgLeggedDeferrals = "NOX_LEGGED_DEFERRALS"

// LeggedDeferral 一条延迟判定的裸露登记。
type LeggedDeferral struct {
	PosID  int64     `json:"pos_id"`
	Filled int       `json:"filled"`
	Total  int       `json:"total"`
	Since  time.Time `json:"since"`
}

type leggedDeferralDoc struct {
	Pending map[string]*LeggedDeferral `json:"pending"`
}

// leggedDeferMu 序列化 load-modify-write（episodeMu 先例；单进程内锁足够——
// finalize 与 reconcile.Run 同进程并发，跨进程并发不在当前部署形态内）。
var leggedDeferMu sync.Mutex

func (s *Store) loadLeggedDeferrals(ctx context.Context) (*leggedDeferralDoc, error) {
	v, ok, err := s.AppConfigString(ctx, cfgLeggedDeferrals)
	if err != nil || !ok {
		return &leggedDeferralDoc{Pending: map[string]*LeggedDeferral{}}, err
	}
	var doc leggedDeferralDoc
	if err := json.Unmarshal([]byte(v), &doc); err != nil {
		return &leggedDeferralDoc{Pending: map[string]*LeggedDeferral{}}, fmt.Errorf("legged deferrals decode: %w", err)
	}
	if doc.Pending == nil {
		doc.Pending = map[string]*LeggedDeferral{}
	}
	return &doc, nil
}

func (s *Store) saveLeggedDeferrals(ctx context.Context, doc *leggedDeferralDoc) error {
	buf, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("legged deferrals encode: %w", err)
	}
	return s.UpsertAppConfig(ctx, cfgLeggedDeferrals, string(buf), "D-175 混合腿 LEGGED 裸露告警延迟判定登记（内部状态键）")
}

// DeferLeggedAlert finalize 混合腿 LEGGED 分支登记延迟判定（替代即时 critical）。
// 同 posID 重登记覆盖（幂等——重跑 LEGGED 分支以最新腿况为准）。
func (s *Store) DeferLeggedAlert(ctx context.Context, posID int64, filled, total int) error {
	leggedDeferMu.Lock()
	defer leggedDeferMu.Unlock()
	doc, err := s.loadLeggedDeferrals(ctx)
	if err != nil {
		return err
	}
	doc.Pending[strconv.FormatInt(posID, 10)] = &LeggedDeferral{
		PosID: posID, Filled: filled, Total: total, Since: time.Now(),
	}
	return s.saveLeggedDeferrals(ctx, doc)
}

// ListLeggedDeferrals reconcile 惰性复查读面（返回副本，调用方不得改写内联）。
func (s *Store) ListLeggedDeferrals(ctx context.Context) ([]LeggedDeferral, error) {
	doc, err := s.loadLeggedDeferrals(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]LeggedDeferral, 0, len(doc.Pending))
	for _, d := range doc.Pending {
		if d != nil {
			out = append(out, *d)
		}
	}
	return out, nil
}

// ClearLeggedDeferral 闭环/宣判后销登记（缺键幂等）。
func (s *Store) ClearLeggedDeferral(ctx context.Context, posID int64) error {
	leggedDeferMu.Lock()
	defer leggedDeferMu.Unlock()
	doc, err := s.loadLeggedDeferrals(ctx)
	if err != nil {
		return err
	}
	delete(doc.Pending, strconv.FormatInt(posID, 10))
	return s.saveLeggedDeferrals(ctx, doc)
}
