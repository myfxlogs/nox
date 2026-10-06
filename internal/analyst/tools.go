package analyst

// tools.go 5 工具 + 安全护栏（设计 §2 工具集，全部只读白名单硬编码）。
// 安全单测必覆盖：query_ro 拒 UPDATE/拒多语句/自动 LIMIT/超时；路径 ../ 逃逸拒/白名单外拒/symlink 逃逸拒；rate 桶满拒。

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"arb/internal/store/pgstore"

	"github.com/jackc/pgx/v5/pgxpool"
)

// KnRoot 容器内挂载根（设计 §2：./docs:/kn/docs:rw + ./internal:/kn/internal:ro + ...）。
const KnRoot = "/kn"

// Tools 工具执行器（实现 ToolExecutor）。
type Tools struct {
	ReadOnlyDSN string         // ARB_AI_PG_DSN（arb_ro 角色，query_ro 专用）
	Store       *pgstore.Store // get_entity 用主 store（已认证连接，只读查询）
	KnRoot      string         // 文件工具根（默认 /kn，测试可覆盖）
	// R2 返修②：position 派生字段回调（dashboard.Service 注入，复用 enrichOne 同源派生）。
	// nil = 未注入（测试/未接线），get_entity position 返回基础字段不含派生。
	EnrichPosition func(ctx context.Context, p *pgstore.Position, legs []pgstore.PositionLeg) map[string]any
}

// 工具名常量。
const (
	toolQueryRO   = "query_ro"
	toolGetEntity = "get_entity"
	toolGrepKB    = "grep_kb"
	toolReadDoc   = "read_doc"
	toolReadCode  = "read_code"
)

// Names 返回工具名集。
func (t *Tools) Names() []string {
	return []string{toolQueryRO, toolGetEntity, toolGrepKB, toolReadDoc, toolReadCode}
}

// Execute 分派工具调用。
func (t *Tools) Execute(ctx context.Context, call ToolCall) (ToolResult, []Citation, error) {
	switch call.Name {
	case toolQueryRO:
		return t.execQueryRO(ctx, call)
	case toolGetEntity:
		return t.execGetEntity(ctx, call)
	case toolGrepKB:
		return t.execGrepKB(ctx, call)
	case toolReadDoc:
		return t.execReadFile(ctx, call, "docs")
	case toolReadCode:
		return t.execReadFile(ctx, call, "internal", "proto", "cmd")
	default:
		return ToolResult{ToolUseID: call.ID, Content: "未知工具: " + call.Name, IsError: true}, nil, nil
	}
}

// ToolSpecs 返回工具定义（供 client.go MessageRequest.Tools 用）。
func ToolSpecs() []ToolSpec {
	return []ToolSpec{
		{Name: toolQueryRO, Description: "只读 SQL 查询（仅 SELECT，arb 库，自动 LIMIT 500，5s 超时）", InputSchema: rawJSON(`{"type":"object","properties":{"sql":{"type":"string","description":"SELECT 语句"}},"required":["sql"]}`)},
		{Name: toolGetEntity, Description: "按 ID 拉实体结构化 JSON（position/opportunity/stat-vkey）", InputSchema: rawJSON(`{"type":"object","properties":{"type":{"type":"string","enum":["position","opportunity","stat-vkey"]},"id":{"type":"string"}},"required":["type","id"]}`)},
		{Name: toolGrepKB, Description: "经验库/decisions/docs 关键词检索（grep -rn）", InputSchema: rawJSON(`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string","description":"子路径如 docs/经验库"}},"required":["pattern"]}`)},
		{Name: toolReadDoc, Description: "读 docs/**/*.md（≤2000 行）", InputSchema: rawJSON(`{"type":"object","properties":{"path":{"type":"string","description":"相对 /kn/docs 的路径"}},"required":["path"]}`)},
		{Name: toolReadCode, Description: "读仓内源码 internal/proto/cmd（≤2000 行）", InputSchema: rawJSON(`{"type":"object","properties":{"path":{"type":"string","description":"相对 /kn 的路径如 internal/analyst/tools.go"}},"required":["path"]}`)},
	}
}

func rawJSON(s string) json.RawMessage { return json.RawMessage(s) }

// --- query_ro ---

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
		return ToolResult{ToolUseID: call.ID, Content: "query_ro 未配置（ARB_AI_PG_DSN 缺失）", IsError: true}, nil, nil
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

// --- read_doc / read_code ---

const maxReadLines = 2000

// execReadFile 读文件（白名单前缀校验 + EvalSymlinks 防符号链接逃逸）。
// allowedSubs 为允许的子目录（相对 KnRoot）。
func (t *Tools) execReadFile(ctx context.Context, call ToolCall, allowedSubs ...string) (ToolResult, []Citation, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(call.Input), &args); err != nil {
		return ToolResult{ToolUseID: call.ID, Content: "参数解析失败: " + err.Error(), IsError: true}, nil, nil
	}
	root := t.KnRoot
	if root == "" {
		root = KnRoot
	}
	full, ok := safePath(root, args.Path, allowedSubs)
	if !ok {
		return ToolResult{ToolUseID: call.ID, Content: "拒绝：路径逃逸或不在白名单内", IsError: true}, nil, nil
	}
	data, err := readFileLines(full, maxReadLines)
	if err != nil {
		return ToolResult{ToolUseID: call.ID, Content: "读取失败: " + err.Error(), IsError: true}, nil, nil
	}
	cite := Citation{Kind: "code", Ref: args.Path, Excerpt: truncate(string(data), 500)}
	kind := "code"
	if len(allowedSubs) == 1 && allowedSubs[0] == "docs" {
		kind = "kb"
	}
	cite.Kind = kind
	return ToolResult{ToolUseID: call.ID, Content: string(data)}, []Citation{cite}, nil
}

// safePath 路径安全校验：filepath.Clean 后前缀校验 + EvalSymlinks 后仍在白名单内。
func safePath(root, rel string, allowedSubs []string) (string, bool) {
	cleanRel := filepath.Clean(rel)
	if filepath.IsAbs(cleanRel) || strings.HasPrefix(cleanRel, "..") {
		return "", false
	}
	full := filepath.Join(root, cleanRel)
	// EvalSymlinks 解析符号链接到真实路径
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", false
	}
	// 校验真实路径仍在 root 下且在 allowedSubs 之一
	relToRoot, err := filepath.Rel(root, real)
	if err != nil || strings.HasPrefix(relToRoot, "..") {
		return "", false
	}
	for _, sub := range allowedSubs {
		if strings.HasPrefix(relToRoot, sub+string(filepath.Separator)) || relToRoot == sub {
			return real, true
		}
	}
	return "", false
}

// --- grep_kb ---

// execGrepKB 递归 grep 限定 /kn 根。旗标用 BusyBox 子集（D-089：alpine 基座无 GNU grep，
// --include 直接 exit 2）——-I 跳二进制、-e 防 pattern 首字符为 -、-l 只列文件；
// .md/.go 限定移到 Go 侧后缀过滤，语义保真（全滤空=无匹配）。
func (t *Tools) execGrepKB(ctx context.Context, call ToolCall) (ToolResult, []Citation, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal([]byte(call.Input), &args); err != nil {
		return ToolResult{ToolUseID: call.ID, Content: "参数解析失败: " + err.Error(), IsError: true}, nil, nil
	}
	if args.Pattern == "" {
		return ToolResult{ToolUseID: call.ID, Content: "pattern 不能为空", IsError: true}, nil, nil
	}
	root := t.KnRoot
	if root == "" {
		root = KnRoot
	}
	searchRoot := root
	if args.Path != "" {
		full, ok := safePath(root, args.Path, []string{"docs", "internal", "proto", "cmd"})
		if !ok {
			return ToolResult{ToolUseID: call.ID, Content: "拒绝：搜索路径逃逸", IsError: true}, nil, nil
		}
		searchRoot = full
	}
	gctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(gctx, "grep", "-rn", "-I", "-l", "-e", args.Pattern, searchRoot)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return ToolResult{ToolUseID: call.ID, Content: "无匹配"}, nil, nil
		}
		return ToolResult{ToolUseID: call.ID, Content: "grep 失败: " + err.Error(), IsError: true}, nil, nil
	}
	// --include 的 .md/.go 限定（D-089）：对输出行按后缀过滤，大小写不敏感。
	var kept []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		low := strings.ToLower(line)
		if strings.HasSuffix(low, ".md") || strings.HasSuffix(low, ".go") {
			kept = append(kept, line)
		}
	}
	files := strings.Join(kept, "\n")
	if files == "" {
		return ToolResult{ToolUseID: call.ID, Content: "无匹配"}, nil, nil
	}
	cite := Citation{Kind: "kb", Ref: truncate(args.Pattern, 100), Excerpt: truncate(files, 500)}
	return ToolResult{ToolUseID: call.ID, Content: files}, []Citation{cite}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
