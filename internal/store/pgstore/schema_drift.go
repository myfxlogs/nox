// schema_drift.go —— 约束漂移侦测锚（carry_graduations 'futures' 缺位事故根治件）：
// 病灶=已应用基线/旧迁移被就地改，新定义永不回流已部署实例（测试库从零跑迁移
// 永远拿新定义=机检结构性盲区）。本件把「迁移链声明的 CHECK 终态」解析为期望集，
// 启动自检时与活库 pg_get_constraintdef 逐一对拍——双向漂移均告警：
//
//	① 链上有定义而活库缺席/定义不符（本事故型）；
//	② 活库存在而链上无对应声明（生产手工改/绕迁移写）。
//
// 期望集构造：migrationFiles 按版本序扫描 baseline+全部迁移文本——
// 命名 CONSTRAINT..CHECK 与列级裸 CHECK 均入集，末次定义胜出（同名覆盖），
// DROP CONSTRAINT 立墓碑、DROP COLUMN 级联删引用它的 CHECK、DROP TABLE 清表。
// 对拍单位=表内规范化定义多重集（名字不参与——列级 CHECK 自动命名 vs 生产手工名
// 不属语义漂移；改名不改义亦不误报）。
package pgstore

import (
	"context"
	"fmt"
	"sort"
)

// driftExemptTables 运行时遗留表豁免集（精确表名——禁宽掩码）：
// ticks_old=D-626 阶段 C rename 回滚件（收口注记72 钦定观察期保留，非迁移链
// 声明面；其 CHECK 随改名表体自带=运行时产物非漂移）。ticks_old 裁弃后本行随删。
var driftExemptTables = map[string]bool{"ticks_old": true}

// CheckDrift 单处 CHECK 约束漂移描述。
type CheckDrift struct {
	Table, Name, Detail string
}

func (d CheckDrift) String() string {
	return fmt.Sprintf("%s.%s: %s", d.Table, d.Name, d.Detail)
}

// SchemaCheckDrift 活库 CHECK 约束定义 vs 迁移链期望终态对拍（启动自检读口；
// 纯读零写，调用侧决定告警/fatal）。返回按 table.name 排序的漂移清单。
func (s *Store) SchemaCheckDrift(ctx context.Context) ([]CheckDrift, error) {
	expected, parts := expectedCheckDefs() // map[table]map[name]normDef
	rows, err := s.pool.Query(ctx, `
		SELECT cl.relname, c.conname, pg_get_constraintdef(c.oid)
		  FROM pg_constraint c
		  JOIN pg_class cl ON cl.oid = c.conrelid
		  JOIN pg_namespace n ON n.oid = cl.relnamespace
		 WHERE c.contype = 'c' AND n.nspname = 'public' AND c.conparentid = 0`)
	if err != nil {
		return nil, fmt.Errorf("live check defs: %w", err)
	}
	defer rows.Close()
	live := map[string][]struct{ name, def string }{}
	for rows.Next() {
		var rel, name, def string
		if err := rows.Scan(&rel, &name, &def); err != nil {
			return nil, fmt.Errorf("live check defs scan: %w", err)
		}
		live[rel] = append(live[rel], struct{ name, def string }{name, normalizeCheckDef(def)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var out []CheckDrift
	tables := map[string]bool{}
	for t := range expected {
		tables[t] = true
	}
	for t := range live {
		if !parts[t] && !driftExemptTables[t] { // 运行时遗留表豁免（精确名）
			tables[t] = true
		}
	}
	for t := range tables {
		liveDefs := map[string][]string{} // normDef → live names
		for _, l := range live[t] {
			liveDefs[l.def] = append(liveDefs[l.def], l.name)
		}
		var expNames []string
		expDefs := map[string]string{}
		for n, d := range expected[t] {
			expNames = append(expNames, n)
			expDefs[n] = d
		}
		sort.Strings(expNames)
		for _, n := range expNames {
			d := expDefs[n]
			if names, ok := liveDefs[d]; ok && len(names) > 0 {
				liveDefs[d] = names[1:]
				continue
			}
			out = append(out, CheckDrift{t, n, "链上有声明而活库 CHECK 缺席或被改写，链上终态=" + d})
		}
		var defs []string
		for d := range liveDefs {
			defs = append(defs, d)
		}
		sort.Strings(defs)
		for _, d := range defs {
			for _, n := range liveDefs[d] {
				out = append(out, CheckDrift{t, n, "活库存在而迁移链无声明（疑手工改/绕迁移写）：" + d})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Table != out[j].Table {
			return out[i].Table < out[j].Table
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}
