// okx_private_ws_heartbeat.go —— 私面 WS 心跳件（返修①静户误杀修）。
package exchange

import (
	"context"
	"log"
	"time"

	"nox/internal/platform/safego"
	"nox/internal/store/pgstore"
)

// heartbeatSnapLoop 心跳环：3min 拍刷末帧快照 fetched_at（WS 活=监护活性
// 真源——account 频道只在余额变动推，静户无推送=快照陈→误杀修）。
func (o *OKXPrivateWS) heartbeatSnapLoop(ctx context.Context) {
	t := time.NewTicker(3 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			o.heartbeatSnap(ctx)
		}
	}
}

// heartbeatSnapLoopSpawn 装配件（Run 内 GoLoopPlain 一行调用）。
func (o *OKXPrivateWS) heartbeatSnapLoopSpawn(ctx context.Context) {
	if !o.hbSpawned.CompareAndSwap(false, true) {
		return // 幂等——重连叠加 goroutine 防（每进程一次，ctx 终随主 ctx 退）
	}
	safego.GoLoopPlain("okx_private_ws:heartbeat-snap", func() { o.heartbeatSnapLoop(ctx) })
}

// heartbeatSnap 静户误杀修：末帧快照刷新 fetched_at（WS 活=监护活性真源；
// 无末帧=未捕获首推如实跳）。3min 拍=快照龄闸 5min 窗内留余。
func (o *OKXPrivateWS) heartbeatSnap(ctx context.Context) {
	r := o.lastSnap.Load()
	if r == nil || o.sink == nil {
		return
	}
	row := *r
	row.FetchedAt = o.now()
	if err := o.sink.InsertVenueAccountSnapshots(ctx, []pgstore.VenueAccountSnapshot{row}); err != nil {
		log.Printf("[okx-private-ws] heartbeat snap: %v", err)
	}
}
