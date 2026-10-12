// retention_d152.go —— D-152：无界表 TTL 扩面（六表挂 janitor 日轮）。
// 照 pruneFactsKind 先例：分批 DELETE（≤pruneTTLBatch/批防长事务/WAL 尖峰）+
// 组终值卫兵（EXISTS 更新兄弟行=非组内最新才删——停发源末读数永存）。
// cex_private_events 分档：证据帧（positions/account 帧 data[]/P[] 含 pos/pa 归零项 +
// close/fill 系频道）90d；非证据心跳帧 7d；其余杂频道 90d。
// 归零判定=Go 侧解析 payload（票面钦定：勿用 SQL LIKE 判 pos 字段），
// pos/pa=="0" 语义与 exec.matchCloseFrame 一致。
package pgstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// pruneTTLBatch 单批删除/扫描上限（十万级——facts 先例）。
const pruneTTLBatch = 100000

// retentionTableTTL D-152 五表窗表：ts 列 + 组终值分组列（IS NOT DISTINCT FROM 处理 NULL）。
// fin_cex_radar 走 PruneCexRadar 委派本轨（票面钦定接线点）不入本表。
// 全部 90d（审计口径见 STATE D-152 行；xasset 全史→≤90d 语义变更 decisions.md 留痕）。
var retentionTableTTL = []struct {
	table, ts string
	group     []string
	days      int
}{
	{"fin_xasset_monitor", "ts", []string{"pair", "cex_venue", "mt_venue"}, 90},
	{"fin_venue_account_snapshot", "fetched_at", []string{"venue", "venue_type"}, 90},
	{"fin_symdig_findings", "run_ts", []string{"venue", "symbol", "klass"}, 90},
	{"fin_trx_account", "fetched_at", []string{"address"}, 90},
}

// pruneTTL 通用分批 TTL 删：ts<cutoff 且非组终值（组=gROUP 列集，存在更新兄弟行才删）。
// 表名/列名=代码内常量（无外部输入）；批量循环至清零。返回删除行数。
func (s *Store) pruneTTL(ctx context.Context, table, tsCol string, group []string, cutoff time.Time) (int64, error) {
	join := ""
	for i, c := range group {
		if i > 0 {
			join += " AND "
		}
		join += fmt.Sprintf("g.%s IS NOT DISTINCT FROM f.%s", c, c)
	}
	q := fmt.Sprintf(`DELETE FROM %s WHERE ctid IN (
		SELECT f.ctid FROM %s f WHERE f.%s < $1
		  AND EXISTS (SELECT 1 FROM %s g WHERE %s AND g.%s > f.%s)
		LIMIT $2)`, table, table, tsCol, table, join, tsCol, tsCol)
	var total int64
	for {
		tag, err := s.pool.Exec(ctx, q, cutoff, pruneTTLBatch)
		if err != nil {
			return total, fmt.Errorf("prune %s: %w", table, err)
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < pruneTTLBatch {
			return total, nil
		}
	}
}

// PruneRetentionTables D-152：五表 TTL 日轮主体——逐表分批删超窗非终值行。
// 返回每表删除行数（日志用）；任一表失败即返 err（已删部分不回滚，幂等下轮续清）。
func (s *Store) PruneRetentionTables(ctx context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	for _, t := range retentionTableTTL {
		n, err := s.pruneTTL(ctx, t.table, t.ts, t.group,
			time.Now().Add(-time.Duration(t.days)*24*time.Hour))
		out[t.table] = n
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// ---------- cex_private_events 分档 ----------

// cexPrivHeartbeatChannels 心跳帧频道（okx positions / binance account）——
// 帧内含归零项=平仓证据（90d）；无归零项=纯心跳（7d）。其余频道一律 90d。
var cexPrivHeartbeatChannels = []string{"positions", "account"}

// cexPrivFrameIsEvidence Go 侧归零判定：payload data[]/P[] 任一项 pos/pa=="0"
// （与 exec.matchCloseFrame 语义一致）。解析失败=保守当证据帧（宁多留不误删）。
func cexPrivFrameIsEvidence(payload []byte) bool {
	var m struct {
		Data []map[string]json.RawMessage `json:"data"`
		P    []map[string]json.RawMessage `json:"P"`
	}
	if json.Unmarshal(payload, &m) != nil {
		return true
	}
	for _, it := range m.Data {
		if privRawStr(it, "pos") == "0" {
			return true
		}
	}
	for _, it := range m.P {
		if privRawStr(it, "pa") == "0" {
			return true
		}
	}
	return false
}

// privRawStr JSON 字段串提取（string/number 双形态宽容；exec.jstrRaw 同式——
// pgstore 禁反向依赖 exec，本地复刻）。
func privRawStr(m map[string]json.RawMessage, k string) string {
	raw, ok := m[k]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}

// PruneCexPrivateEvents D-152 分档 prune：
// ①全频道 received_at<90d 顶格分批删；②心跳频道 received_at<7d 行 id 游标分批
// 拉取 → Go 归零判定 → 非证据帧删（证据帧留至 90d 顶格自然回收）。返回删行数。
func (s *Store) PruneCexPrivateEvents(ctx context.Context) (int64, error) {
	var total int64
	// ① 90d 顶格（全频道统一上限，心跳/证据/杂频道一闸）。
	cut90 := time.Now().Add(-90 * 24 * time.Hour)
	for {
		tag, err := s.pool.Exec(ctx, `DELETE FROM cex_private_events WHERE ctid IN (
			SELECT ctid FROM cex_private_events WHERE received_at < $1 LIMIT $2)`,
			cut90, pruneTTLBatch)
		if err != nil {
			return total, fmt.Errorf("prune cex_private_events 90d: %w", err)
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < pruneTTLBatch {
			break
		}
	}
	// ② 心跳帧 7d：id 游标整扫（保留帧随游标前推不重扫），批内非证据帧即删。
	cut7 := time.Now().Add(-7 * 24 * time.Hour)
	var lastID int64
	for {
		rows, err := s.pool.Query(ctx, `SELECT id, payload FROM cex_private_events
			WHERE channel = ANY($1) AND received_at < $2 AND id > $3
			ORDER BY id LIMIT 5000`, cexPrivHeartbeatChannels, cut7, lastID)
		if err != nil {
			return total, fmt.Errorf("prune cex_private_events scan: %w", err)
		}
		var drop []int64
		fetched := 0
		for rows.Next() {
			var id int64
			var payload []byte
			if err := rows.Scan(&id, &payload); err != nil {
				rows.Close()
				return total, fmt.Errorf("prune cex_private_events row: %w", err)
			}
			fetched++
			lastID = id
			if !cexPrivFrameIsEvidence(payload) {
				drop = append(drop, id)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return total, fmt.Errorf("prune cex_private_events rows: %w", err)
		}
		rows.Close()
		if len(drop) > 0 {
			tag, err := s.pool.Exec(ctx,
				`DELETE FROM cex_private_events WHERE id = ANY($1)`, drop)
			if err != nil {
				return total, fmt.Errorf("prune cex_private_events heartbeat: %w", err)
			}
			total += tag.RowsAffected()
		}
		if fetched < 5000 {
			break // 游标到顶
		}
	}
	return total, nil
}
