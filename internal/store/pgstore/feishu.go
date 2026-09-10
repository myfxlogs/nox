// D-264 飞告警推送目标存取（单行表 feishu_push_target）+ InsertAlert 推送钩子。
package pgstore

import (
	"context"
)

// UpsertFeishuTarget 绑定/更新推送目标（单行 id=1）。
func (s *Store) UpsertFeishuTarget(ctx context.Context, chatID, openID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE feishu_push_target SET chat_id = $1, open_id = $2, updated_at = now() WHERE id = 1
	`, chatID, openID)
	if err != nil {
		return err
	}
	return nil
}

// FeishuChatID 读推送目标 chat_id（空=未绑定）。
func (s *Store) FeishuChatID(ctx context.Context) (string, error) {
	var chatID string
	err := s.pool.QueryRow(ctx, `SELECT chat_id FROM feishu_push_target WHERE id = 1`).Scan(&chatID)
	if err != nil {
		return "", err
	}
	return chatID, nil
}

// FeishuTarget 读推送目标 chat_id+open_id（空=未绑定）。
// D-270：确认流程需同时读 open_id 做发送者校验。
func (s *Store) FeishuTarget(ctx context.Context) (chatID, openID string, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT chat_id, open_id FROM feishu_push_target WHERE id = 1`).Scan(&chatID, &openID)
	return
}
