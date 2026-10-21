// schema_drift_scan.go —— 约束漂移期望集构造件（schema_drift.go 配套，300 行棘轮拆分）：
// migrationFiles 全链 SQL 线性扫描 → 表→(约束名→规范化终态)；命名/列级裸 CHECK、
// DROP 墓碑、DROP COLUMN 级联、分区豁免、NOT VALID、注释/引号串跳过、
// 平衡括号抓取、deparse 形态归一化全在本文件。
package pgstore

import (
	"regexp"
	"strings"
)

var (
	reCreateTable   = regexp.MustCompile(`(?i)^CREATE\s+(?:UNLOGGED\s+|TEMP\s+|TEMPORARY\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(?:public\.)?(\w+)`)
	reAlterTable    = regexp.MustCompile(`(?i)^ALTER\s+TABLE\s+(?:ONLY\s+)?(?:public\.)?(\w+)`)
	reDropTable     = regexp.MustCompile(`(?i)^DROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:public\.)?(\w+)`)
	reConstraintChk = regexp.MustCompile(`(?i)^(?:ADD\s+)?CONSTRAINT\s+(\w+)\s+CHECK`)
	reBareChk       = regexp.MustCompile(`(?i)^CHECK\b`)
	reDropCon       = regexp.MustCompile(`(?i)^DROP\s+CONSTRAINT\s+(?:IF\s+EXISTS\s+)?(\w+)`)
	reDropCol       = regexp.MustCompile(`(?i)^DROP\s+COLUMN\s+(?:IF\s+EXISTS\s+)?(\w+)`)
	reRenameCon     = regexp.MustCompile(`(?i)^RENAME\s+CONSTRAINT\s+(\w+)\s+TO\s+(\w+)`)
	reAttachPart    = regexp.MustCompile(`(?i)^ATTACH\s+PARTITION\s+(?:public\.)?(\w+)`)
	rePartitionOf   = regexp.MustCompile(`(?i)^PARTITION\s+OF\b`)
	reNotValid      = regexp.MustCompile(`(?i)^\s*NOT\s+VALID`)
	reInList        = regexp.MustCompile(`(?i)\bIN\s*\(`)
	reIdent         = regexp.MustCompile(`[a-zA-Z_]\w*`)
	reSingleAny     = regexp.MustCompile(`= ANY ARRAY\[('[^']*')\]`)
)

// expectedCheckDefs 解析 migrationFiles 全链 SQL，产出 table→(name→规范化期望定义)
// + 分区表集合（PARTITION OF 声明/ATTACH PARTITION 挂靠——成员表随分区维护
// 动态生灭，其 CHECK 继承自父表，双向均不入对拍）。
func expectedCheckDefs() (map[string]map[string]string, map[string]bool) {
	defs := map[string]map[string]string{}
	parts := map[string]bool{}
	for _, m := range migrationFiles {
		scanCheckSQL(m.sql, defs, parts)
	}
	for t := range parts {
		delete(defs, t)
	}
	return defs, parts
}

func defPut(defs map[string]map[string]string, table, name, def string) {
	if defs[table] == nil {
		defs[table] = map[string]string{}
	}
	for n, d := range defs[table] {
		if d == def {
			delete(defs[table], n) // 同定义去重（列级裸 CHECK 与命名 CONSTRAINT 同源二录）
		}
	}
	defs[table][name] = def
}

func defDelColRef(defs map[string]map[string]string, table, col string) {
	word := regexp.MustCompile(`\b` + regexp.QuoteMeta(col) + `\b`)
	for n, d := range defs[table] {
		if word.MatchString(d) {
			delete(defs[table], n)
		}
	}
}

// scanCheckSQL 线性扫描单文件：表头更新当前表；CHECK 抓平衡括号段（串内括号
// 不配平）；DROP CONSTRAINT/COLUMN/TABLE 与 RENAME CONSTRAINT 同步期望集；
// -- 行注释与 /* */ 块注释跳过；裸 CHECK 按 pg 自动命名约定 <table>_<首列>_check
// 落名（后续迁移按名 DROP 才能正确立墓碑）。
func scanCheckSQL(sql string, defs map[string]map[string]string, parts map[string]bool) {
	cur := ""
	i := 0
	for i < len(sql) {
		if sql[i] == '\'' {
			i = skipString(sql, i)
			continue
		}
		if sql[i] == '-' && i+1 < len(sql) && sql[i+1] == '-' {
			if nl := strings.IndexByte(sql[i:], '\n'); nl >= 0 {
				i += nl + 1
				continue
			}
			break
		}
		if sql[i] == '/' && i+1 < len(sql) && sql[i+1] == '*' {
			if e := strings.Index(sql[i+2:], "*/"); e >= 0 {
				i += e + 4
				continue
			}
			break
		}
		if m, end := matchAt(reCreateTable, sql, i); m != nil {
			cur, i = m[0], end
			continue
		}
		if m, end := matchAt(reAlterTable, sql, i); m != nil {
			cur, i = m[0], end
			continue
		}
		if m, end := matchAt(reAttachPart, sql, i); m != nil {
			parts[m[0]] = true
			i = end
			continue
		}
		if _, end := matchAt(rePartitionOf, sql, i); end > i {
			if cur != "" {
				parts[cur] = true
			}
			i = end
			continue
		}
		if m, end := matchAt(reDropTable, sql, i); m != nil {
			delete(defs, m[0])
			i = end
			continue
		}
		if cur == "" {
			i++
			continue
		}
		if m, end := matchAt(reRenameCon, sql, i); m != nil {
			old, newn := m[0], m[1]
			if d, ok := defs[cur][old]; ok {
				defs[cur][newn] = d
				delete(defs[cur], old)
			}
			i = end
			continue
		}
		if m, end := matchAt(reDropCon, sql, i); m != nil {
			delete(defs[cur], m[0])
			i = end
			continue
		}
		if m, end := matchAt(reDropCol, sql, i); m != nil {
			defDelColRef(defs, cur, m[0])
			i = end
			continue
		}
		if m, end := matchAt(reConstraintChk, sql, i); m != nil {
			i = scanOneCheck(sql, end, func(def string, firstCol string) {
				defPut(defs, cur, m[0], def)
			})
			continue
		}
		if _, end := matchAt(reBareChk, sql, i); end > i {
			i = scanOneCheck(sql, end, func(def string, firstCol string) {
				name := cur + "_" + firstCol + "_check" // pg 列级 CHECK 自动命名约定
				defPut(defs, cur, name, def)
			})
			continue
		}
		i++
	}
}

// scanOneCheck 于 i 处（CHECK 关键字之后）定位 '(' 抓平衡段并交 cb
// （第二参=表达式内首个标识符——列级 CHECK 自动命名所需的列名）。返回推进下标。
func scanOneCheck(sql string, i int, cb func(string, string)) int {
	j := strings.IndexByte(sql[i:], '(')
	if j < 0 {
		return i + 1
	}
	j += i
	end := matchParen(sql, j)
	if end < 0 {
		return len(sql)
	}
	def := sql[j : end+1]
	if reNotValid.MatchString(sql[end+1:]) {
		def += " NOT VALID"
	}
	first := ""
	if m := reIdent.FindString(def); m != "" {
		first = strings.ToLower(m)
	}
	cb(normalizeCheckDef(def), first)
	return end + 1
}

// matchAt 在 i 处锚定匹配，返回捕获组（reRenameCon 为双组）与匹配右端。
func matchAt(re *regexp.Regexp, s string, i int) ([]string, int) {
	loc := re.FindStringSubmatchIndex(s[i:])
	if loc == nil || loc[0] != 0 {
		return nil, 0
	}
	var groups []string
	for g := 2; g+1 < len(loc); g += 2 {
		if loc[g] >= 0 {
			groups = append(groups, s[i+loc[g]:i+loc[g+1]])
		}
	}
	return groups, i + loc[1]
}

// skipString 跳过单引号串（” 为转义）。
func skipString(s string, i int) int {
	i++
	for i < len(s) {
		if s[i] == '\'' {
			if i+1 < len(s) && s[i+1] == '\'' {
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return i
}

// matchParen 返回与 s[i]=='(' 配平的右括号下标（串内括号不计），失配返回 -1。
func matchParen(s string, i int) int {
	depth := 0
	for j := i; j < len(s); j++ {
		if s[j] == '\'' {
			j = skipString(s, j) - 1
			continue
		}
		switch s[j] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return -1
}

// normalizeCheckDef 双向规范化归一到同一形态再比，防写法差异误报：
// 剥 CHECK 前缀 → IN (..) 重写为 = ANY (ARRAY[..]) → LIKE 族→~~ 族
// （NOT LIKE→!~~ / ILIKE→~~* / NOT ILIKE→!~~*，pg_get_constraintdef 形态）
// → ::cast 脱壳 → 空白折叠 → 剥全部圆括号（token 流比对——deparse 对运算元
// 的括号包裹与源码写法天然不一，语义等价类内不比括号结构）。
func normalizeCheckDef(def string) string {
	out := strings.TrimSpace(def)
	out = regexp.MustCompile(`(?i)^CHECK\b`).ReplaceAllString(out, "")
	out = strings.TrimSpace(out)
	for {
		loc := reInList.FindStringIndex(out)
		if loc == nil {
			break
		}
		open := loc[1] - 1
		close := matchParen(out, open)
		if close < 0 {
			break
		}
		out = out[:loc[0]] + "= ANY (ARRAY[" + out[open+1:close] + "])" + out[close+1:]
	}
	out = regexp.MustCompile(`(?i)\bNOT\s+ILIKE\b`).ReplaceAllString(out, "!~~*")
	out = regexp.MustCompile(`(?i)\bNOT\s+LIKE\b`).ReplaceAllString(out, "!~~")
	out = regexp.MustCompile(`(?i)\bILIKE\b`).ReplaceAllString(out, "~~*")
	out = regexp.MustCompile(`(?i)\bLIKE\b`).ReplaceAllString(out, "~~")
	out = regexp.MustCompile(`!=`).ReplaceAllString(out, "<>")
	out = regexp.MustCompile(`::\w+(\[\])?`).ReplaceAllString(out, "")
	out = strings.ReplaceAll(out, "(", "")
	out = strings.ReplaceAll(out, ")", "")
	out = strings.Join(strings.Fields(out), " ")
	out = strings.ReplaceAll(out, ", ", ",")
	out = reSingleAny.ReplaceAllString(out, "= $1") // deparse 单元素 IN→= 'x'
	return out
}
