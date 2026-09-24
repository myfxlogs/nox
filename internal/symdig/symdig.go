// Package symdig D-464 S2：配对池深挖——每 MT venue 品种四分类 diff 作业（Go 全确定性）。
// L2 分类：tradable+paired / tradable+unpaired(原因) / disabled / unmapped_unknown；
// 附已配对校验（同 venue 同 canonical 多 is_primary → mispaired）。
// 结果落 fin_symdig_findings + 新增 unpaired 聚合 info 告警 + 报告件。
package symdig

import (
	"context"
	"fmt"
	"log"
	"time"

	"arb/internal/analyst"
	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// Store symdig 依赖面（pgstore.Store 实现；测试 fake）。
type Store interface {
	LatestFacts(ctx context.Context, kind, venue, symbol string) ([]fact.Fact, error)
	ListSymbolMappings(ctx context.Context, venue string) ([]pgstore.SymbolMapping, error)
	ListSymbolAliases(ctx context.Context) (map[string]string, error)
	ListCarryUniversePairs(ctx context.Context) (map[string]bool, error)
	LatestSymdigKeys(ctx context.Context) (map[string]bool, error)
	InsertSymdigFindings(ctx context.Context, runTS time.Time, rows []pgstore.SymdigFinding) error
	InsertAlert(ctx context.Context, ruleID *int64, level, message string) error
}

// Deps 一轮深挖依赖（Now/路径可注入；Client 可空=跳过 LLM 段）。
type Deps struct {
	Store     Store
	Client    *analyst.Client
	Run       analystRunner
	Now       func() time.Time
	ReportDir string
	InboxPath string
}

// analystRunner 复用 sysretro/kbreport 同形态 runner seam（analyst.RunWithSpecs）。
type analystRunner = func(ctx context.Context, client *analyst.Client, tools analyst.ToolExecutor,
	specs []analyst.ToolSpec, system string, msgs []analyst.Message, cb analyst.LoopCallbacks) (analyst.LoopResult, error)

func (d *Deps) fill() {
	if d.Now == nil {
		d.Now = func() time.Time { return time.Now().UTC() }
	}
	if d.Run == nil {
		d.Run = analyst.RunWithSpecs
	}
}

// Summary 一轮分类聚合（报告/告警用）。
type Summary struct {
	Rows        []pgstore.SymdigFinding
	NewUnpaired []string // 较上一轮新增的 unpaired/unmapped/mispaired 键
	ByKlass     map[string]int
}

// tradableMode trade_mode∈{Full,LongOnly,ShortOnly}=可开新仓（CloseOnly/Disabled 拦）。
func tradableMode(mode int64) bool {
	return mode == fact.TradeModeFull || mode == fact.TradeModeLongOnly || mode == fact.TradeModeShortOnly
}

// Round 一轮深挖：分类→落库→告警→报告→LLM 提案（best-effort 分段，单段失败不炸整轮）。
func Round(ctx context.Context, deps Deps) (*Summary, error) {
	deps.fill()
	if deps.Store == nil {
		return nil, fmt.Errorf("symdig: store 未配置")
	}
	runTS := deps.Now().UTC()
	sum, err := classify(ctx, deps.Store)
	if err != nil {
		return nil, err
	}
	if err := deps.Store.InsertSymdigFindings(ctx, runTS, sum.Rows); err != nil {
		return nil, fmt.Errorf("insert findings: %w", err)
	}
	if len(sum.NewUnpaired) > 0 {
		msg := fmt.Sprintf("[symdig] 新增未配对/错配 %d: %s", len(sum.NewUnpaired), joinLimit(sum.NewUnpaired, 10))
		if err := deps.Store.InsertAlert(ctx, nil, "info", msg); err != nil {
			log.Printf("[symdig] alert: %v", err)
		}
	}
	if path, err := writeReport(deps.ReportDir, runTS, sum); err != nil {
		log.Printf("[symdig] report: %v", err)
	} else {
		log.Printf("[symdig] report: %s", path)
	}
	if deps.Client != nil && deps.InboxPath != "" {
		if err := llmDig(ctx, deps, sum, runTS); err != nil {
			log.Printf("[symdig] llm dig: %v", err)
		}
	}
	return sum, nil
}
