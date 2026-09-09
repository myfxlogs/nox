// D-264 测试：feishu_push_target 单行表读写。
package pgstore

import (
	"context"
	"testing"
)

func TestFeishuTargetRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	// 迁移插入的空行：chat_id 空
	got, err := s.FeishuChatID(ctx)
	if err != nil {
		t.Fatalf("FeishuChatID empty: %v", err)
	}
	if got != "" {
		t.Fatalf("initial chat_id = %q, want empty", got)
	}
	if err := s.UpsertFeishuTarget(ctx, "oc_p2p", "ou_me"); err != nil {
		t.Fatalf("UpsertFeishuTarget: %v", err)
	}
	got, err = s.FeishuChatID(ctx)
	if err != nil {
		t.Fatalf("FeishuChatID: %v", err)
	}
	if got != "oc_p2p" {
		t.Errorf("chat_id = %q, want oc_p2p", got)
	}
}
