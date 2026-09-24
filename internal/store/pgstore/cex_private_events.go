// cex_private_events.go —— D-450 S4：CEX 私有 WS 事件审计表写面。
// 两 venue WS 事件一律先落此表再更新内存缓存（审计+回放源；JSONB 原文保留）。
package pgstore

import "context"

// InsertCEXPrivateEvent 落一行私有 WS 事件原文。payload 须为合法 JSON。
func (s *Store) InsertCEXPrivateEvent(ctx context.Context, venue, venueType, channel, eventKey string, payload []byte) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO cex_private_events (venue, venue_type, channel, event_key, payload) VALUES ($1,$2,$3,$4,$5)`,
		venue, venueType, channel, eventKey, payload)
	return err
}
