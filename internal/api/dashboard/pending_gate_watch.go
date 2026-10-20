// pending_gate_watch.go —— D-007 S1：待入场闸态 diff 器（展示面失效触发器首案）。
// 病灶：ready/waitReason 等派生列=RPC 请求时现算（probeGates+fillWaitCondition 读
// qbus+monitor+caps），服务端无组件 diff 其输出——「闸态变了」在系统里不是事件，
// 前端无推送可收（冷窗文案「qbus未接…」无限期冻结实证）。
// 本器把翻转制成事件：三源标脏 → 尾沿合批重估受影响组合 → (ready,waitReason)
// 元组与上拍 diff → 变了才 PublishDataRefresh("pending_entries")。
//   - qbus 帧：canonical→combo 索引命中即标脏（只对受影响组合重估，非全表）；
//   - RefreshAllowRules 写事件：组合/白名单/敞口闸配置变更 → 全量标脏；
//   - 看门狗 60s：报价流断线→tick 超龄/快照超龄/monitor 超龄翻转捕手
//     （§5 定时器唯一合法形态：检测流断线触发重估，不产业务数据）。
//
// 翻转驱动非逐 tick：1s 尾沿合批（泵内多帧/多组合合并成一次 emit）。
package dashboard

import (
	"context"
	"fmt"
	"sync"
	"time"

	"nox/internal/infra/dephealth"
)

// pendingGateFlushInterval 尾沿合批窗（帧洪峰合并成单次 emit；对齐 fundsNotifier 形态）。
const pendingGateFlushInterval = time.Second

// pendingGateStaleScan 超龄翻转捕手档（tick 5min/快照 5min/monitor 30min 超龄边界无帧可触，
// 唯一捕手=周期重估；看门狗定位见文件头）。
const pendingGateStaleScan = time.Minute

// pendingGateWatcher 闸态 diff 器状态（Service 内嵌懒初始化——测试字面量构造零配）。
type pendingGateWatcher struct {
	mu             sync.Mutex
	last           map[int64]string   // comboID → 上拍 (ready|waitReason) 元组串
	dirty          map[int64]struct{} // 标脏组合集（帧源）
	dirtyAll       bool               // 全量标脏（配置写/看门狗/boot 基线）
	index          map[string][]int64 // canonical → 引用它的 enabled 组合（帧源路由表，flush 重建）
	flushPending   bool               // AfterFunc 在挂（串行链：单 flush 不并发）
	lastFlush      time.Time
	primed         bool          // boot 首拍已基线化（首拍只建档不推）
	flushEvery     time.Duration // 测试可缩（默认 pendingGateFlushInterval）
	staleScanEvery time.Duration // 测试可缩（默认 pendingGateStaleScan）
	now            func() time.Time
	svcRef         *Service
}

// pgWatch 懒初始化（nil-safe：部分构造 Service 不装配 watcher 也无害）。
func (s *Service) pgWatch() *pendingGateWatcher {
	s.pgwOnce.Do(func() {
		s.pgw = &pendingGateWatcher{
			svcRef:     s,
			last:       map[int64]string{},
			dirty:      map[int64]struct{}{},
			index:      map[string][]int64{},
			flushEvery: pendingGateFlushInterval, staleScanEvery: pendingGateStaleScan,
			now: time.Now,
		}
	})
	return s.pgw
}

// RunPendingGateWatch 装配入口（cmd/core safego 托管；qbus/store 未接=静默退役）。
func (s *Service) RunPendingGateWatch(ctx context.Context) {
	if s == nil || s.qbus == nil || s.store == nil {
		return
	}
	w := s.pgWatch()
	qch, qcancel := s.qbus.Subscribe("") // 全量帧 → canonical 索引过滤（受影响组合才标脏）
	defer qcancel()
	var rch <-chan string
	if s.refreshTopic != nil {
		var rcancel func()
		rch, rcancel = s.refreshTopic.Subscribe()
		defer rcancel()
	}
	w.markAll()
	w.kick(ctx) // boot 基线：建档不推（primed=false 吞首拍）
	watchdog := time.NewTicker(w.staleScanEvery)
	defer watchdog.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case q, ok := <-qch:
			if !ok {
				return
			}
			w.markBySymbol(q.Symbol)
		case t, ok := <-rch:
			if !ok {
				rch = nil
				continue
			}
			if t == RefreshAllowRules {
				w.markAll()
			}
		case <-watchdog.C:
			w.markAll()
			// S3′-c 真心跳：闸评活着的证据=watch 环每拍重扫（非有 opp 帧）。
			dephealth.Mark("gate_eval:watch", true, nil)
		}
		w.kick(ctx)
	}
}

func (w *pendingGateWatcher) markAll() {
	w.mu.Lock()
	w.dirtyAll = true
	w.mu.Unlock()
}

func (w *pendingGateWatcher) markBySymbol(sym string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, id := range w.index[sym] {
		w.dirty[id] = struct{}{}
	}
}

// kick 尾沿合批调度（fundsNotifier 同形态：间隔内首标即挂 AfterFunc，末帧必被冲刷）。
func (w *pendingGateWatcher) kick(ctx context.Context) {
	w.mu.Lock()
	if w.flushPending {
		w.mu.Unlock()
		return
	}
	delay := w.flushEvery - w.now().Sub(w.lastFlush)
	if delay < 0 {
		delay = 0
	}
	w.flushPending = true
	w.mu.Unlock()
	time.AfterFunc(delay, func() {
		w.mu.Lock()
		w.flushPending = false
		w.lastFlush = w.now()
		w.mu.Unlock()
		w.flush(ctx)
	})
}

// flush 重估标脏组合→元组 diff→变了才 emit（单次 emit 承载一批变更，前端整表静默重取）。
func (w *pendingGateWatcher) flush(ctx context.Context) {
	s := w.svc()
	if s == nil {
		return
	}
	combos, err := s.store.ListXAssetAllowCombosAll(ctx)
	if err != nil {
		return // 标脏保留，下个触发再试（fail-silent→下轮，不写库不告警=观察性组件）
	}
	in, err := s.loadPendingInputs(ctx)
	if err != nil {
		return
	}
	w.mu.Lock()
	dirtyAll, dirty := w.dirtyAll, w.dirty
	w.dirtyAll, w.dirty = false, map[int64]struct{}{}
	// 重建 canonical→combo 路由索引（enabled 组合集；帧源按此定向标脏）。
	idx := map[string][]int64{}
	for _, c := range combos {
		if !c.Enabled {
			continue
		}
		idx[c.CanonicalBuy] = append(idx[c.CanonicalBuy], c.ID)
		idx[c.CanonicalSell] = append(idx[c.CanonicalSell], c.ID)
	}
	w.index = idx
	w.mu.Unlock()

	changed := false
	seen := map[int64]bool{}
	// 行级死期：eval 内多查询持连链不可无界——watch ctx 是 root 裸传，
	// 再现池 wedge 时持连永久（10-20 事故实证形态）。每行 20s 熔断放行。
	for _, c := range combos {
		if !dirtyAll {
			if _, hit := dirty[c.ID]; !hit {
				continue
			}
		}
		rctx, rcancel := context.WithTimeout(ctx, 20*time.Second)
		row, _, ok := s.evalPendingRow(rctx, c, in)
		rcancel()
		w.mu.Lock()
		if !ok {
			// 退出待入场集（disabled/在持仓）→ 展示集成员变化=也要推。
			if _, had := w.last[c.ID]; had {
				delete(w.last, c.ID)
				changed = true
			}
			w.mu.Unlock()
			continue
		}
		seen[c.ID] = true
		tup := fmt.Sprintf("%v|%s", row.Ready, row.WaitReason)
		if prev, had := w.last[c.ID]; !had || prev != tup {
			w.last[c.ID] = tup
			changed = true
		}
		w.mu.Unlock()
	}
	if dirtyAll {
		w.mu.Lock()
		for id := range w.last { // 全量拍才可信清理成员（增量拍 seen 不全）
			if !seen[id] {
				delete(w.last, id)
				changed = true
			}
		}
		w.mu.Unlock()
	}
	w.mu.Lock()
	primed := w.primed
	w.primed = true
	w.mu.Unlock()
	if changed && primed { // boot 首拍只建档——否则启动恒推一次噪音
		s.PublishDataRefresh(RefreshPendingEntries)
	}
}

// svc 持有 Service 反引（watcher 不复制依赖，单源在 Service）。
func (w *pendingGateWatcher) svc() *Service {
	return w.svcRef
}
