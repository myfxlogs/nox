// ws_guard.go —— D-370 S1-S3（R1 拆分）：Session expired 完整重登缝 + 新鲜度守卫
// （独立于 marketOpen、1-strike、每 tick 日志）+ 断流告警（unhealthy>10min，30min 冷却）
// 自 ws.go 整组移出（同包纯搬移零行为变更；check-lines 超线修法）。
package cmc

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// D-370 S2/S3：断流告警阈值（unhealthy 持续 >10min 即告）+ 同指纹冷却 30min
const (
	wsAlertAfterStale = 10 * time.Minute
	wsAlertCooldown   = 30 * time.Minute
)

// maybeRelogin D-370 S1：Session expired 命中 → 完整 re-Login（relogin 缝；测试注入点）。
// 返回是否命中（Run 循环用于日志分流）。
func (w *WsClient) maybeRelogin(ctx context.Context, err error) bool {
	if !isSessionExpired(err) || w.relogin == nil {
		return false
	}
	if lerr := w.relogin(ctx); lerr != nil {
		log.Printf("[cmc-ws] re-login after session expired: %v", lerr)
	} else {
		log.Printf("[cmc-ws] session expired → full re-login ok")
	}
	return true
}

// isSessionExpired D-370 S1：CMC auth ack 无独立错误码，status 字符串 "Session expired"
// 为失效特征（生产实采 Session expired×18）；"auth ack status=" 前缀=结构性 session 拒绝
// 双保险（错误码优先+字符串 fallback）。
func isSessionExpired(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Session expired") || strings.HasPrefix(msg, "auth ack status=")
}

func (w *WsClient) startGuard(ctx context.Context) {
	ticker := time.NewTicker(wsGuardInterval)
	defer ticker.Stop()
	var unhealthySince, lastAlert time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.guardTick(ctx, &unhealthySince, &lastAlert)
		}
	}
}

// guardTick D-370 S2/S3 单 tick：新鲜度判定（1 次 stale 即 unhealthy——R4 前为 2 次且被
// marketOpen 门控静默跳过=生产 0 触发根因）+ 每 tick 日志（R4 前零取证）+ unhealthy 持续
// >10min 断流告警（同指纹 30min 冷却，经 alerts 表）。传输层健康与市场时段无关——
// marketOpen 门控摘除（守卫不再依赖 DB 残留 status）。
func (w *WsClient) guardTick(ctx context.Context, unhealthySince, lastAlert *time.Time) {
	w.lastEvent6Mu.Lock()
	since := time.Since(w.lastEvent6)
	last6 := w.lastEvent6
	w.lastEvent6Mu.Unlock()
	healthy := since <= wsGuardStale
	w.healthy.Store(healthy)
	log.Printf("[cmc-ws] guard: lastType6=%v ago, healthy=%v", since.Round(time.Second), healthy)
	if healthy {
		*unhealthySince = time.Time{}
		return
	}
	now := time.Now()
	if unhealthySince.IsZero() {
		*unhealthySince = now
	}
	if now.Sub(*unhealthySince) > wsAlertAfterStale && now.Sub(*lastAlert) >= wsAlertCooldown {
		msg := fmt.Sprintf("[cmc-ws] 断流 %v（last type6 at %s UTC）",
			now.Sub(*unhealthySince).Round(time.Second), last6.UTC().Format("2006-01-02 15:04:05"))
		if err := w.store.InsertAlert(ctx, nil, "warn", msg); err != nil {
			log.Printf("[cmc-ws] 断流告警写入失败: %v", err)
		} else {
			*lastAlert = now
			log.Printf("%s", msg)
		}
	}
}
