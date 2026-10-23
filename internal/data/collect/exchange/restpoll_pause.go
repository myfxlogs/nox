// restpoll_pause.go —— venue REST 轮询暂停开关（NOX_REST_POLL_PAUSED_<venue>
// app_config 键=1 停）。仅 REST 拍面；WS/earn/持仓零动——venue 面收窄应急闸。
package exchange

import (
	"context"
	"log"
	"time"
)

// restPollPaused 判当前 venue REST 拍是否暂停（PauseFn nil/err=不暂停
// fail-open——配置面读败不停采集）。
func restPollPaused(ctx context.Context, cfg Config, venue string) bool {
	if cfg.RestPollPausedFn == nil {
		return false
	}
	p, err := cfg.RestPollPausedFn(ctx, venue)
	if err != nil {
		log.Printf("[%s-ws] rest poll pause 读败（fail-open 不暂停）: %v", venue, err)
		return false
	}
	return p
}

// waitRestPollResume 暂停期 60s 轮候恢复（ctx.Done 早退返 false——loop 出口）。
func waitRestPollResume(ctx context.Context) bool {
	t := time.NewTimer(60 * time.Second)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
