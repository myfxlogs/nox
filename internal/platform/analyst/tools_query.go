package analyst

// tools_query.go —— query_ro 工具件（原 tools.go 拆分，零行为变）。

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var selectRe = regexp.MustCompile(`(?i)^\s*SELECT\b`)

// execQueryRO 只读 SQL：正则 ^SELECT 单语句（拒含 ; 二次语句）；无 LIMIT 自动补 LIMIT 500；5s 超时。
func (t *Tools) execQueryRO(ctx context.Context, call ToolCall) (ToolResult, []Citation, error) {
	var args struct {
		SQL string `json:"sql"`
	}
	if err := json.Unmarshal([]byte(call.Input), &args); err != nil {
		return ToolResult{ToolUseID: call.ID, Content: "参数解析失败: " + err.Error(), IsError: true}, nil, nil
	}
	q := strings.TrimSpace(args.SQL)
	if !selectRe.MatchString(q) {
		return ToolResult{ToolUseID: call.ID, Content: "拒绝：仅允许 SELECT 语句", IsError: true}, nil, nil
	}
	if strings.Contains(q, ";") {
		return ToolResult{ToolUseID: call.ID, Content: "拒绝：禁止多语句（含分号）", IsError: true}, nil, nil
	}
	if !strings.Contains(strings.ToUpper(q), "LIMIT") {
		q += " LIMIT 500"
	}
	if t.ReadOnlyDSN == "" {
		return ToolResult{ToolUseID: call.ID, Content: "query_ro 未配置（NOX_AI_PG_DSN 缺失）", IsError: true}, nil, nil
	}
	// R1 返修②：改用 pgxpool（贴仓内惯例，pgxpool.ParseConfig 天然注册驱动，无 database/sql "unknown driver" 坑）。
	pool, err := pgxpool.New(ctx, t.ReadOnlyDSN)
	if err != nil {
		return ToolResult{ToolUseID: call.ID, Content: "连接只读 PG 失败: " + err.Error(), IsError: true}, nil, nil
	}
	defer pool.Close()
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := pool.Query(qctx, q)
	if err != nil {
		return ToolResult{ToolUseID: call.ID, Content: "查询失败: " + err.Error(), IsError: true}, nil, nil
	}
	defer rows.Close()
	fields := rows.FieldDescriptions()
	cols := make([]string, len(fields))
	for i, f := range fields {
		cols[i] = f.Name
	}
	result := []map[string]any{}
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			continue
		}
		row := map[string]any{}
		for i, c := range cols {
			if i < len(vals) {
				row[c] = vals[i]
			}
		}
		result = append(result, row)
	}
	out, _ := json.Marshal(map[string]any{"rows": result, "count": len(result)})
	cite := Citation{Kind: "sql", Ref: truncate(q, 200), Excerpt: fmt.Sprintf("%d 行", len(result))}
	return ToolResult{ToolUseID: call.ID, Content: string(out)}, []Citation{cite}, nil
}
