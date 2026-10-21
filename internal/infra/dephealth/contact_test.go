// contact_test.go —— 接触面犬哨锚：零命中超阈→嫌疑集+命中→无嫌疑双锚。
package dephealth

import (
	"testing"
	"time"
)

func TestContactWatch_DualAnchor(t *testing.T) {
	ContactRegister("t_chan")
	// 未命中通道=嫌疑（Hits=0 从未命中）。
	found := false
	for _, r := range ContactSuspects(72 * time.Hour) {
		if r.Channel == "t_chan" {
			found = true
		}
	}
	if !found {
		t.Fatal("零命中通道应入嫌疑集")
	}
	// 命中后清嫌疑。
	ContactHit("t_chan")
	for _, r := range ContactSuspects(72 * time.Hour) {
		if r.Channel == "t_chan" {
			t.Fatalf("命中后不应嫌疑: %+v", r)
		}
	}
	// 命中计数+last_hit 可见。
	for _, r := range ContactSnapshot() {
		if r.Channel == "t_chan" && (r.Hits != 1 || r.LastHitUnix == 0) {
			t.Fatalf("快照行错: %+v", r)
		}
	}
	// 未注册命中忽略（同 rejects 哨兵——不建态）。
	ContactHit("unregistered_xyz")
	for _, r := range ContactSnapshot() {
		if r.Channel == "unregistered_xyz" {
			t.Fatal("未注册通道不应建态")
		}
	}
	_ = time.Now // 免未用
}
