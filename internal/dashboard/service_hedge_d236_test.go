// D-236 施工：对冲卡信息分区六条源文本断言（先红后绿）。
// ①投票年化/流动/能量/带宽文本位于 hedge-acct-block 渲染段
// ②强平格 title 含「平台口径」与两净值文本
// ③质押格不含「流动」
// ④tsx 源文本不含「秒前」且不含「现价(ask)」（D-236 变更11 翻转：头部价格行已删）
// ⑤App.tsx→PositionsPage→HedgeStructure 的 cmcWatch prop 链不存在（D-236 变更11 翻转）
// ⑥APR 渲染含 floor 截断（Math.floor）且无 toFixed(2) 残留于 APR 三数
// ⑪镜像双行加开仓价/现价 2 列（D-236 变更10；业主令 9-6 现货腿开仓价改用 baselinePrice）
// ⑬现货腿浮盈=对冲镜像（D-236 变更13；legSpot.PnlVsBase = -ls.PnL 后端算，tsx 读 legSpot?.pnlVsBase）
package dashboard

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"
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

// 断言④（D-236 变更11 翻转）：tsx 源文本不含「秒前」且不含「现价(ask)」（头部价格行已删）。
func TestD236_PriceRowSecondsAgo(t *testing.T) {
	src := hedgeStructureSource(t)
	if strings.Contains(src, "秒前") {
		t.Fatal("tsx 源文本仍含「秒前」（D-236 变更11 头部价格行应删）")
	}
	if strings.Contains(src, "现价(ask)") {
		t.Fatal("tsx 源文本仍含「现价(ask)」（D-236 变更11 头部价格行应删）")
	}
}

// 断言⑤（D-236 变更11 翻转）：App.tsx→PositionsPage→HedgeStructure 的 cmcWatch prop 链不存在。
func TestD236_AppCmcWatchProp(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	for _, rel := range []string{"App.tsx", "components/PositionsPage.tsx", "components/PositionsPageHedgeStructure.tsx"} {
		path := filepath.Join(filepath.Dir(testFile), "..", "..", "web", "src", rel)
		src := readFile(t, path)
		if strings.Contains(src, "cmcWatch") {
			t.Errorf("%s 仍含 cmcWatch（D-236 变更11 prop 链应退役）", rel)
		}
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

// 断言⑪：镜像双行加开仓价/现价 2 列（D-236 变更10；业主令 9-6 现货腿开仓价改用 baselinePrice）。
// thead 含两列 th，列序=数量→开仓价→现价→浮动盈亏；
// 空单腿行含 baselinePrice 引用、现价表达式与头部 ask || structure!.mid 同源；
// 现货腿行开仓价=baselinePrice（业主令 9-6：CMC 开仓价），现价同空单腿表达式。
func TestD236_EntryPriceCurrentPriceColumns(t *testing.T) {
	src := hedgeStructureSource(t)
	// ① thead 含开仓价/现价两列 th
	if !strings.Contains(src, `<th scope="col">开仓价</th>`) {
		t.Fatal(`thead 缺 <th scope="col">开仓价</th>`)
	}
	if !strings.Contains(src, `<th scope="col">现价</th>`) {
		t.Fatal(`thead 缺 <th scope="col">现价</th>`)
	}
	// ② 列序：数量 → 开仓价 → 现价 → 浮动盈亏
	qtyIdx := strings.Index(src, `<th scope="col">数量</th>`)
	entryIdx := strings.Index(src, `<th scope="col">开仓价</th>`)
	curIdx := strings.Index(src, `<th scope="col">现价</th>`)
	pnlIdx := strings.Index(src, `<th scope="col">浮动盈亏</th>`)
	if !(qtyIdx < entryIdx && entryIdx < curIdx && curIdx < pnlIdx) {
		t.Fatal("列序不符：应为 数量→开仓价→现价→浮动盈亏")
	}
	// ③ 空单腿行含 baselinePrice 引用（开仓价=基准同源），现价与头部同源
	shortIdx := strings.Index(src, `legShort?.label`)
	if shortIdx < 0 {
		t.Fatal("无法定位空单腿行")
	}
	shortEnd := strings.Index(src[shortIdx:], "</tr>")
	if shortEnd < 0 {
		t.Fatal("无法定位空单腿行结束")
	}
	shortRow := src[shortIdx : shortIdx+shortEnd]
	if !strings.Contains(shortRow, "baselinePrice") {
		t.Error("空单腿行缺 baselinePrice 引用（开仓价应=基准同源）")
	}
	if !strings.Contains(shortRow, "ask || structure!.mid") {
		t.Error("空单腿行缺 ask || structure!.mid（现价应与头部同源）")
	}
	// ④ 现货腿行开仓价=baselinePrice（业主令 9-6：CMC 开仓价），现价同空单腿表达式
	spotIdx := strings.Index(src, `legSpot?.label`)
	if spotIdx < 0 {
		t.Fatal("无法定位现货腿行")
	}
	spotEnd := strings.Index(src[spotIdx:], "</tr>")
	if spotEnd < 0 {
		t.Fatal("无法定位现货腿行结束")
	}
	spotRow := src[spotIdx : spotIdx+spotEnd]
	if !strings.Contains(spotRow, "baselinePrice") {
		t.Error("现货腿行缺 baselinePrice（业主令 9-6：开仓价应=CMC 开仓价）")
	}
	if !strings.Contains(spotRow, "ask || structure!.mid") {
		t.Error("现货腿行缺 ask || structure!.mid（现价应与头部同源）")
	}
}

// 断言⑬（D-236 变更13）：现货腿浮盈=对冲镜像。
// service_hedge.go 源文本含 `legSpot.PnlVsBase = -`（镜像运算在后端，零信任）；
// tsx 现货腿行含 `legSpot?.pnlVsBase` 且该行不含 `legShort` 引用。
func TestD236_SpotLegPnlMirror(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	hedgePath := filepath.Join(filepath.Dir(testFile), "service_hedge.go")
	hedgeSrc := readFile(t, hedgePath)
	if !strings.Contains(hedgeSrc, "legSpot.PnlVsBase = -") {
		t.Fatal("service_hedge.go 缺 `legSpot.PnlVsBase = -`（D-236 变更13 现货腿浮盈=对冲镜像，后端算零信任）")
	}
	src := hedgeStructureSource(t)
	spotIdx := strings.Index(src, `legSpot?.label`)
	if spotIdx < 0 {
		t.Fatal("无法定位现货腿行")
	}
	spotEnd := strings.Index(src[spotIdx:], "</tr>")
	if spotEnd < 0 {
		t.Fatal("无法定位现货腿行结束")
	}
	spotRow := src[spotIdx : spotIdx+spotEnd]
	if !strings.Contains(spotRow, "legSpot?.pnlVsBase") {
		t.Error("现货腿行缺 `legSpot?.pnlVsBase`（D-236 变更13 应读镜像字段）")
	}
	if strings.Contains(spotRow, "legShort") {
		t.Error("现货腿行仍含 legShort 引用（D-236 变更13 应改读 legSpot?.pnlVsBase）")
	}
}

// --- D-244 变更12 断言⑫（先红后绿；TRX 页第一性重构） ---

// 断言⑫a：AppConstants 含 trx tab（TABS 键 + Tab type）。
func TestD244_AppConstantsTrxTab(t *testing.T) {
	_, testFile, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(testFile), "..", "..", "web", "src", "AppConstants.ts")
	src := readFile(t, path)
	if !strings.Contains(src, `key: "trx"`) {
		t.Errorf("AppConstants.ts TABS 缺 { key: \"trx\" }（变更12）")
	}
	if !strings.Contains(src, `"trx"`) || !strings.Contains(src, `| "trx"`) {
		t.Errorf("AppConstants.ts Tab type 缺 \"trx\"（变更12）")
	}
}

// 断言⑫b：App.tsx 挂 TrxPage；PositionsPage 渲染块不再透传 hedgeStructure。
func TestD244_AppTrxPageMount(t *testing.T) {
	_, testFile, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(testFile), "..", "..", "web", "src")
	app := readFile(t, filepath.Join(dir, "App.tsx"))
	if !strings.Contains(app, `tab === "trx"`) || !strings.Contains(app, "<TrxPage") {
		t.Errorf("App.tsx 缺 trx tab 渲染块（变更12）")
	}
	pos := readFile(t, filepath.Join(dir, "components", "PositionsPage.tsx"))
	if strings.Contains(pos, "hedgeStructure={") {
		t.Errorf("PositionsPage 仍透传 hedgeStructure（应迁 TrxPage）")
	}
	if strings.Contains(pos, "hedgeFlow={") {
		t.Errorf("PositionsPage 仍透传 hedgeFlow（应迁 TrxPage）")
	}
}

// 断言⑫c：PositionsPage 不再 import 两迁移组件；TrxPage 三组件齐挂。
func TestD244_ComponentMigration(t *testing.T) {
	_, testFile, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(testFile), "..", "..", "web", "src")
	pos := readFile(t, filepath.Join(dir, "components", "PositionsPage.tsx"))
	if strings.Contains(pos, "PositionsPageHedgeStructure") || strings.Contains(pos, "PositionsPageHedgeFlow") {
		t.Errorf("PositionsPage 仍引用迁移组件（应全迁 TrxPage）")
	}
	trx := readFile(t, filepath.Join(dir, "components", "TrxPage.tsx"))
	for _, comp := range []string{"PositionsPageHedgeStructure", "PositionsPageHedgeFlow", "PositionsPageDailyIncome"} {
		if !strings.Contains(trx, "<"+comp) {
			t.Errorf("TrxPage 缺挂载 %s（变更12 三组件）", comp)
		}
	}
}

// 断言⑫d：对冲卡表单段退役；收益区组件自带 listDailyIncome。
func TestD244_FormRelocated(t *testing.T) {
	_, testFile, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(testFile), "..", "..", "web", "src")
	hs := readFile(t, filepath.Join(dir, "components", "PositionsPageHedgeStructure.tsx"))
	if strings.Contains(hs, "addDailyIncome") {
		t.Errorf("对冲卡仍含内嵌收益表单（应迁 PositionsPageDailyIncome）")
	}
	di := readFile(t, filepath.Join(dir, "components", "PositionsPageDailyIncome.tsx"))
	if !strings.Contains(di, "listDailyIncome") {
		t.Errorf("DailyIncome 组件缺 listDailyIncome（迁移载体失效）")
	}
}

// --- D-236 变更14 断言⑭（先红后绿；年化公式时间感知+诚实空态） ---

// 断言⑭a：service_hedge.go 年化改时间感知（days 因子除法），旧式单因子式退役。
func TestD236_v14_TimeAwareAnnualization(t *testing.T) {
	_, testFile, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(testFile), "..", "..", "internal", "dashboard", "service_hedge.go")
	src := readFile(t, path)
	if !strings.Contains(src, "FirstDailyIncomeDay") {
		t.Errorf("service_hedge.go 缺 FirstDailyIncomeDay（首收益日=时间感知分母）")
	}
	if !strings.Contains(src, "/ days") {
		t.Errorf("service_hedge.go 缺 days 因子除法（时间感知年化）")
	}
	oldForm := "cumUsd / (qtyStaked * mid) * 365 * 100"
	if strings.Contains(src, oldForm) {
		t.Errorf("旧式单因子年化式仍存在（累计当单日收益=荒谬值根因）")
	}
}

// 断言⑭b：前端 carry 行年化空态「—」（annualized≤0 渲染 —，不渲染 0.00% 伪值）。
func TestD236_v14_AnnualizedEmptyState(t *testing.T) {
	_, testFile, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(testFile), "..", "..", "web", "src", "components", "PositionsPageHedgeStructure.tsx")
	src := readFile(t, path)
	if !strings.Contains(src, `年化 —`) && !strings.Contains(src, `"—"}{{"`) {
		if !strings.Contains(src, "annualizedPct > 0") {
			t.Errorf("carry 行缺年化空态分支（annualizedPct>0 才渲染百分数）")
		}
	}
}

// 单测（变更14 机检）：构造已知 days 断言年化值（DSN 门控）。
func TestD236_v14_CarryAnnualizationMath(t *testing.T) {
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	st, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgstore: %v", err)
	}
	t.Cleanup(st.Close)
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM position_daily_income WHERE day >= '2099-01-01'`)
	})
	// 种子：唯一收益日=10 天前（energy 3650 TRX），interest=10 USD。mid=0.33，qty=1000。
	day := time.Now().UTC().Add(-10 * 24 * time.Hour)
	day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	if _, err := st.AddDailyIncome(ctx, day, 3650, 0, 10.0, nil, nil, "d236v14"); err != nil {
		t.Fatalf("seed income: %v", err)
	}
	mid, qty := 0.33, 1000.0
	s := &Service{store: st}
	// 期望天数按实现同口径：读回 MIN(day)（UTC 午夜截断），days=now−firstDay（非整 10）——
	// 本测验证「公式=时间感知」而非日期边界算术。
	firstDay, err := st.FirstDailyIncomeDay(ctx)
	if err != nil || firstDay == nil {
		t.Fatalf("read first day: %v", err)
	}
	days := time.Since(*firstDay).Hours() / 24
	if days < 1 {
		days = 1
	}
	cumExpected := 3650*mid + 10.0
	annualExpected := cumExpected / (qty * mid) / days * 365 * 100
	carry := s.computeHedgeCarry(ctx, mid, qty)
	if diff := math.Abs(carry.CumUsd - cumExpected); diff > 0.01 {
		t.Fatalf("cum = %v, want ≈%v", carry.CumUsd, cumExpected)
	}
	if diff := math.Abs(carry.AnnualizedPct - annualExpected); diff > annualExpected*0.001 {
		t.Fatalf("annualized = %v, want ≈%v (±0.1%%, days=%.3f)", carry.AnnualizedPct, annualExpected, days)
	}
}

// --- D-236 变更15 断言⑮（先红后绿；对冲前后分段年化） ---

// 断言⑮a：service_hedge.go 含第二窗口计算（from=hedgeStart，FirstCMCPositionOpenedAt）。
func TestD236_v15_HedgedSecondWindow(t *testing.T) {
	_, testFile, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(testFile), "..", "..", "internal", "dashboard", "service_hedge.go")
	src := readFile(t, path)
	if !strings.Contains(src, "FirstCMCPositionOpenedAt") {
		t.Errorf("service_hedge.go 缺 FirstCMCPositionOpenedAt（对冲起点）")
	}
	if !strings.Contains(src, "hedgedCumUsd") {
		t.Errorf("service_hedge.go 缺第二窗口累计（hedgedCumUsd）")
	}
}

// 断言⑮b：proto HedgeCarry 含 hedged_annualized_pct 字段。
func TestD236_v15_ProtoHedgedFields(t *testing.T) {
	_, testFile, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(testFile), "..", "..", "proto", "arb", "dashboard", "v1", "dashboard.proto")
	src := readFile(t, path)
	if !strings.Contains(src, "hedged_annualized_pct") {
		t.Errorf("proto 缺 hedged_annualized_pct（变更15）")
	}
	if !strings.Contains(src, "hedged_cum_usd") {
		t.Errorf("proto 缺 hedged_cum_usd（变更15）")
	}
}

// 断言⑮c：前端含「对冲后」行。
func TestD236_v15_FrontendHedgedRow(t *testing.T) {
	_, testFile, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(testFile), "..", "..", "web", "src", "components", "PositionsPageHedgeStructure.tsx")
	src := readFile(t, path)
	if !strings.Contains(src, "对冲后") {
		t.Errorf("carry 统计条缺「对冲后」分段行（变更15）")
	}
}
