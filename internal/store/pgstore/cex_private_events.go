// cex_private_events.go —— D-450 S4：CEX 私有 WS 事件审计表写面 + D-577 读面。
// 两 venue WS 事件一律先落此表再更新内存缓存（审计+回放源；JSONB 原文保留）。
package pgstore

import (
	"context"
	"encoding/json"
	"time"
)

// InsertCEXPrivateEvent 落一行私有 WS 事件原文。payload 须为合法 JSON。
func (s *Store) InsertCEXPrivateEvent(ctx context.Context, venue, venueType, channel, eventKey string, payload []byte) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO cex_private_events (venue, venue_type, channel, event_key, payload) VALUES ($1,$2,$3,$4,$5)`,
		venue, venueType, channel, eventKey, payload)
	return err
}

// CexPrivateEvent D-577：私有 WS 事件审计行读面（回填源=venue 推送已持久化帧）。
type CexPrivateEvent struct {
	Channel    string
	EventKey   string
	Payload    json.RawMessage
	ReceivedAt time.Time
}

// ListCexPrivateEventsSince 按 venue+venue_type+channel+received_at>=since 读私有事件审计行。
func (s *Store) ListCexPrivateEventsSince(ctx context.Context, venue, venueType, channel string, since time.Time) ([]CexPrivateEvent, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT channel, event_key, payload, received_at FROM cex_private_events WHERE venue=$1 AND venue_type=$2 AND channel=$3 AND received_at>=$4 ORDER BY received_at`,
		venue, venueType, channel, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CexPrivateEvent
	for rows.Next() {
		var ev CexPrivateEvent
		if err := rows.Scan(&ev.Channel, &ev.EventKey, &ev.Payload, &ev.ReceivedAt); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}
