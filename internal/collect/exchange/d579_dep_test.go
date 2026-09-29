package exchange

import (
	"context"
	"testing"
	"time"

	"arb/internal/dephealth"
)

func depRow(key string) (dephealth.Row, bool) {
	for _, r := range dephealth.Snapshot() {
		if r.Key == key {
			return r, true
		}
	}
	return dephealth.Row{}, false
}

// TestD579_T2_WSDepMark wsrunner 标成败：dial 成功→Mark ok（healthy）；
// runWSOnceSt 返错→Mark fail（suspect 起计）。
func TestD579_T2_WSDepMark(t *testing.T) {
	// 成功面：真 ws 回显服务，拨号成功读循环前标 healthy。
	keyOK := "t2ok:ws-market"
	frames := make(chan []byte, 8)
	url, _ := wsRunnerServer(t, false, frames)
	spec := wsRunnerSpec(url, frames)
	spec.DepKey = keyOK
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunWS(ctx, spec)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if r, ok := depRow(keyOK); ok && r.State == dephealth.StateHealthy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial 成功应标 healthy: %+v", dephealth.Snapshot())
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()

	// 失败面：不可达地址→RunWS 出错分支记 fail（runWSOnce 直调不记账——挂点在 RunWS 环）。
	keyFail := "t2fail:ws-market"
	bad := wsRunnerSpec("ws://127.0.0.1:1", frames)
	bad.DepKey = keyFail
	bad.ReconnectBackoff = 10 * time.Millisecond
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go RunWS(ctx2, bad)
	deadline = time.Now().Add(3 * time.Second)
	for {
		if r, ok := depRow(keyFail); ok && r.ConsecFails >= 1 {
			if r.State != dephealth.StateSuspect {
				t.Fatalf("连败未达阈应 suspect: %+v", r)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("失败应记账: %+v", dephealth.Snapshot())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel2()
}
