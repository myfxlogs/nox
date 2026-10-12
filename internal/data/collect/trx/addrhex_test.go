package trx

import "testing"

// 止血5 对拍锚（D-210 审计实证对）：TBtvnRB ↔ 4115210c…
func TestHexToBase58_TBtvnRB(t *testing.T) {
	got, err := hexToBase58("4115210cb63f8e554a21c63973443d74d694790fa6")
	if err != nil {
		t.Fatalf("hexToBase58: %v", err)
	}
	if got != "TBtvnRBgrVE9zZWkWfPvDSiuo65es55555" {
		t.Errorf("got %s, want TBtvnRBgrVE9zZWkWfPvDSiuo65es55555", got)
	}
}

func TestHexToBase58_MonitorAddr(t *testing.T) {
	got, err := hexToBase58("41e54af280b5c5ccab9275a36b69371c1a38ff3c85") // 业主地址 hex（base58 本地复算+校验过）
	if err != nil {
		t.Fatalf("hexToBase58: %v", err)
	}
	if got != "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555" {
		t.Errorf("got %s, want TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", got)
	}
}

func TestNormalizeAddr_PassThrough(t *testing.T) {
	if normalizeAddr("TWsbXR6SPvvXPxUtPMQvEj7AMH55555555") != "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555" {
		t.Error("base58 应原样返回")
	}
}
