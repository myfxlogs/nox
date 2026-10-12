package pgstore

// facts_keydomain.go —— D-496 键域宪法：facts 写入侧 (venue,venue_type) 推导 + R4 symbol NULL 归位。

import (
	"context"

	"nox/internal/data/keydomain"
)

// mtVenueTypes broker_accounts venue→venue_type（facts 裸账号号的通道型反解表；
// broker_accounts 小表 ~12 行，COPY 批次一次查询可忽略成本）。
func (s *Store) mtVenueTypes(ctx context.Context) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT venue, venue_type FROM broker_accounts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var v, t string
		if err := rows.Scan(&v, &t); err != nil {
			return nil, err
		}
		out[v] = t
	}
	return out, rows.Err()
}

// factsVenueType D-496 R2：裸 venue → venue_type 推导（与迁移 0142 回填规则同表）。
func factsVenueType(vk keydomain.VenueKey, mtTypes map[string]string) string {
	if vk.Venue == "local" {
		return "" // 本机观测源（D-104 egress_canary 等 local:* 复合键）非交易通道，先于后缀分支
	}
	if vk.Type != "" {
		return vk.Type // 生产方传复合键（如 deribit:option）→ 后缀即类型
	}
	if isDigits(vk.Venue) {
		if t, ok := mtTypes[vk.Venue]; ok {
			return t
		}
		return "mt5" // 注册处缺号 → mt5（既有默认语义，fail-open 仅类型列）
	}
	if vk.Venue == "cmc" {
		return "" // 行情数据源非交易通道
	}
	return "perp" // CEX 裸名 fact 域=perp（funding/swap_settled 实拍 kind 闭集）
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
