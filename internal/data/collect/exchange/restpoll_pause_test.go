// restpoll_pause_test.go —— REST 采集暂停开关锚：nil/err fail-open、真值判停。
package exchange

import (
	"context"
	"errors"
	"testing"
)

func TestRestPollPaused_Gates(t *testing.T) {
	ctx := context.Background()
	if restPollPaused(ctx, Config{}, "gate") { // nil fn=不暂停
		t.Fatal("nil PauseFn 应不暂停")
	}
	on := Config{RestPollPausedFn: func(_ context.Context, _ string) (bool, error) { return true, nil }}
	if !restPollPaused(ctx, on, "binance") {
		t.Fatal("开关真值应判停")
	}
	off := Config{RestPollPausedFn: func(_ context.Context, _ string) (bool, error) { return false, errors.New("x") }}
	if restPollPaused(ctx, off, "gate") { // 读败 fail-open 不暂停
		t.Fatal("读败 fail-open 应不暂停")
	}
}
