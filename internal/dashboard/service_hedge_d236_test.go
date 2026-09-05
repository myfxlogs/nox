// D-236 施工：对冲卡信息分区六条源文本断言（先红后绿）。
// ①投票年化/流动/能量/带宽文本位于 hedge-acct-block 渲染段
// ②强平格 title 含「平台口径」与两净值文本
// ③质押格不含「流动」
// ④现价行含「秒前」
// ⑤App.tsx 含 cmcWatch 透传
// ⑥APR 渲染含 floor 截断（Math.floor）且无 toFixed(2) 残留于 APR 三数
package dashboard

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func hedgeStructureSource(t *testing.T) string {
	t.Helper()
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(testFile), "..", "..", "web", "src", "components", "PositionsPageHedgeStructure.tsx")
	return readFile(t, path)
}

// fileCache 缓存文件内容，避免 -race 并行测试下并发 os.ReadFile 触发 EBADF（CI 实证）。
var fileCache sync.Map

// readFile 读取文件内容为字符串（测试辅助，断言用；sync.Map 缓存保每文件只读一次）。
func readFile(t *testing.T, path string) string {
	t.Helper()
	if v, ok := fileCache.Load(path); ok {
		return v.(string)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	s := string(data)
	fileCache.Store(path, s)
	return s
}

// 断言①：投票年化/能量/带宽文本位于 hedge-acct-block 渲染段且「流动」行不存在（D-236 变更6 删流动行）。
func TestD236_AccountInfoInAcctBlock(t *testing.T) {
	src := hedgeStructureSource(t)
	idx := strings.Index(src, "hedge-acct-block")
	if idx < 0 {
		t.Fatal("hedge-acct-block 段不存在")
	}
	block := src[idx:]
	for _, want := range []string{"能量", "带宽", "投票年化"} {
		if !strings.Contains(block, want) {
			t.Errorf("hedge-acct-block 段缺文本 %q", want)
		}
	}
	// D-236 变更6：流动行已删（qtyLiquid≡BalanceSun÷1e6 与余额同源同值，重复无信息量）
	if strings.Contains(block, "流动") {
		t.Error("hedge-acct-block 段仍含「流动」行（D-236 变更6 应删）")
	}
}

// 断言②：强平格 title 含「平台口径」与两净值文本（收入 title 悬停，非分散在不同 span 元素）。
func TestD236_CloseoutTitle(t *testing.T) {
	src := hedgeStructureSource(t)
	for _, want := range []string{"平台口径", "强平净值", "预警净值"} {
		if !strings.Contains(src, want) {
			t.Errorf("源文本缺 %q", want)
		}
	}
	// 改前：平台口径与强平净值分散在不同 span/br 元素中（之间有标签间隔）
	// 改后：三文本收入同一 title 字符串（之间无 span/br 标签）
	platIdx := strings.Index(src, "平台口径")
	strongIdx := strings.Index(src, "强平净值")
	if platIdx < 0 || strongIdx < 0 {
		t.Fatal("源文本缺平台口径或强平净值")
	}
	between := src[platIdx:strongIdx]
	if strings.Contains(between, "<span") || strings.Contains(between, "</span") || strings.Contains(between, "<br") {
		t.Error("平台口径与强平净值仍分散在不同元素，未收入同一 title")
	}
}

// 断言③：质押格（legSpot 区域）不含「流动」。
func TestD236_StakeCellNoLiquid(t *testing.T) {
	src := hedgeStructureSource(t)
	stakeIdx := strings.Index(src, "质押")
	blockIdx := strings.Index(src, "hedge-acct-block")
	if stakeIdx < 0 || blockIdx < 0 || stakeIdx >= blockIdx {
		t.Fatal("无法定位质押格与 hedge-acct-block 段")
	}
	legSpotRegion := src[stakeIdx:blockIdx]
	if strings.Contains(legSpotRegion, "流动") {
		t.Fatal("质押格（legSpot 区域）不应含「流动」")
	}
}

// 断言④：现价行含「秒前」。
func TestD236_PriceRowSecondsAgo(t *testing.T) {
	src := hedgeStructureSource(t)
	if !strings.Contains(src, "秒前") {
		t.Fatal("现价行缺「秒前」时戳")
	}
}

// 断言⑤：App.tsx 含 cmcWatch 透传。
func TestD236_AppCmcWatchProp(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(testFile), "..", "..", "web", "src", "App.tsx")
	src := readFile(t, path)
	if !strings.Contains(src, "cmcWatch") {
		t.Fatal("App.tsx 缺 cmcWatch 透传")
	}
}

// 断言⑥：APR 渲染含 floor 截断（Math.floor）且无 toFixed(2) 残留于 APR 三数。
func TestD236_APRFloorTruncation(t *testing.T) {
	src := hedgeStructureSource(t)
	if !strings.Contains(src, "Math.floor") {
		t.Fatal("APR 渲染缺 Math.floor 截断")
	}
	for _, line := range strings.Split(src, "\n") {
		if strings.Contains(line, "投票年化") && strings.Contains(line, "toFixed(2)") {
			t.Fatalf("APR 行残留 toFixed(2): %s", strings.TrimSpace(line))
		}
	}
}

// 断言⑦：13-daily.css 含 grid-template-columns 两列与 1/-1 跨列规则（D-236 变更5 两列 grid）。
func TestD236_CSSGridTwoColumns(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(testFile), "..", "..", "web", "src", "styles", "13-daily.css")
	src := readFile(t, path)
	if !strings.Contains(src, "grid-template-columns") {
		t.Fatal("13-daily.css 缺 grid-template-columns（D-236 变更5 两列 grid）")
	}
	if !strings.Contains(src, "1 / -1") {
		t.Fatal("13-daily.css 缺 1 / -1 跨列规则（D-236 变更5 年化行跨双列）")
	}
}

// 断言⑧：LiveState 单访问器存在（含 Ask/Mid/Qty/Lev/PnL/Cash/Equity/Margin+双 Ts），
// EvalAsk 无独立实现（退役或薄委托），快照/case13 均走 freshLive。
func TestD236_LiveStateSingleAccessor(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// ① cmc 包含 LiveState 结构（Ask/Mid/Qty/Lev/PnL/Cash/Equity/Margin+PosTs/SumTs）
	// D-236 变更9：LiveState 可能在 ws.go 或 ws_live.go（拆分保 ws.go ≤300 行）
	cmcDir := filepath.Join(filepath.Dir(testFile), "..", "..", "internal", "collect", "cmc")
	wsSrc := readFile(t, filepath.Join(cmcDir, "ws.go"))
	wsLiveSrc := readFile(t, filepath.Join(cmcDir, "ws_live.go"))
	cmcSrc := wsSrc + "\n" + wsLiveSrc
	for _, want := range []string{"type LiveState struct", "Ask, Mid, Qty, Lev, PnL", "Cash, Equity, Margin", "PosTs", "SumTs", "func (w *WsClient) LiveState()"} {
		if !strings.Contains(cmcSrc, want) {
			t.Errorf("cmc 包缺 %q（LiveState 单访问器）", want)
		}
	}
	// ② EvalAsk 退役为薄委托（实现体调 LiveState()，无独立 priceMu 锁读）
	evalIdx := strings.Index(cmcSrc, "func (w *WsClient) EvalAsk()")
	if evalIdx < 0 {
		t.Fatal("cmc 包缺 EvalAsk 方法（应保留为薄委托）")
	}
	evalEnd := strings.Index(cmcSrc[evalIdx:], "\n}")
	evalBody := cmcSrc[evalIdx : evalIdx+evalEnd]
	if !strings.Contains(evalBody, "LiveState()") {
		t.Error("EvalAsk 未委托 LiveState()（应有 ls := w.LiveState() 薄委托）")
	}
	if strings.Contains(evalBody, "w.priceMu.Lock()") {
		t.Error("EvalAsk 仍有独立 priceMu 锁读（应退役为薄委托）")
	}

	// ③ dashboard 单 seam SetCmcLiveProvider + freshLive 解析器
	settersPath := filepath.Join(filepath.Dir(testFile), "service_setters.go")
	settersSrc := readFile(t, settersPath)
	if !strings.Contains(settersSrc, "SetCmcLiveProvider") {
		t.Error("service_setters.go 缺 SetCmcLiveProvider（单 seam）")
	}
	watchPath := filepath.Join(filepath.Dir(testFile), "service_cmc_watch.go")
	watchSrc := readFile(t, watchPath)
	if !strings.Contains(watchSrc, "func (s *Service) freshLive(") {
		t.Error("service_cmc_watch.go 缺 freshLive 解析器")
	}
	// 旧 SetCmcAskProvider 函数定义应已退役（注释提及旧名允许，但不应有 func 定义）
	if strings.Contains(settersSrc, "func (s *Service) SetCmcAskProvider") {
		t.Error("service_setters.go 仍含 SetCmcAskProvider 函数定义（应退役替换为 SetCmcLiveProvider）")
	}

	// ④ 快照（GetHedgeStructure）与 case13（computeCMCWatchUpdate）均走 freshLive
	hedgePath := filepath.Join(filepath.Dir(testFile), "service_hedge.go")
	hedgeSrc := readFile(t, hedgePath)
	if !strings.Contains(hedgeSrc, "s.freshLive(ctx)") {
		t.Error("service_hedge.go GetHedgeStructure 缺 freshLive 调用（快照应走 freshLive）")
	}
	if !strings.Contains(watchSrc, "s.freshLive(ctx)") {
		t.Error("service_cmc_watch.go computeCMCWatchUpdate 缺 freshLive 调用（case13 应走 freshLive）")
	}
	// 旧 freshEvalAsk 函数定义应已退役（注释提及旧名允许，但不应有 func 定义）
	if strings.Contains(watchSrc, "func (s *Service) freshEvalAsk(") {
		t.Error("service_cmc_watch.go 仍含 freshEvalAsk 函数定义（应退役替换为 freshLive）")
	}
}

// 断言⑨：broker 列 th/td 不存在（D-236 变更7 删 broker 列）。
func TestD236_NoBrokerColumn(t *testing.T) {
	src := hedgeStructureSource(t)
	if strings.Contains(src, "broker 浮盈") {
		t.Fatal("PositionsPageHedgeStructure.tsx 仍含 broker 浮盈 th（D-236 变更7 应删）")
	}
	if strings.Contains(src, "brokerUnrealised") {
		t.Fatal("PositionsPageHedgeStructure.tsx 仍含 brokerUnrealised td（D-236 变更7 应删）")
	}
}

// 断言⑩：卡片 CMC 字段源文本无 LastCMCAccountSummary/LastCMCPriceMidWithTime 作为主路径（兜底分支除外）。
func TestD236_NoDBPrimaryPath(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	hedgePath := filepath.Join(filepath.Dir(testFile), "service_hedge.go")
	hedgeSrc := readFile(t, hedgePath)
	// LastCMCAccountSummary 允许作为兜底（alerts/closeout_price DB 兜底），但不应作为主路径
	// 主路径=GetHedgeStructure 的 mid/ask/cmcCash/cmcEquity 应走 freshLive，不读 LastCMCPriceMidWithTime
	if strings.Contains(hedgeSrc, "LastCMCPriceMidWithTime") {
		t.Error("service_hedge.go 仍含 LastCMCPriceMidWithTime（D-236 变更9 应改 freshLive，mid 不再读 DB price）")
	}
	watchPath := filepath.Join(filepath.Dir(testFile), "service_cmc_watch.go")
	watchSrc := readFile(t, watchPath)
	if strings.Contains(watchSrc, "LastCMCPriceMidWithTime") {
		t.Error("service_cmc_watch.go 仍含 LastCMCPriceMidWithTime（D-236 变更9 应改 freshLive，mid 不再读 DB price）")
	}
}
