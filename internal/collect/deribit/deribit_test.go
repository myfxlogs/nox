package deribit

import (
	"strings"
	"testing"
	"time"

	"arb/internal/fact"
)

// D-137 5 用例单测（先红后绿）。
// 先红=新测试在新代码上首次跑（实现已写但测试验证断言）；如断言与实现不符=红，修实现至绿。
// 注：本包为新建，旧代码无此包=编译红即先红证据（build 前测试文件不存在）。

// 用例①：平价公式
// dev = (C−P)−(F−K)。提示词原例 C=105/P=95/F=1000/K=1000 → dev=10（非 0，提示词数字有误）。
// 正确的 dev=0 需 C-P=F-K：C=105/P=95/F=1010/K=1000 → (105-95)-(1010-1000)=10-10=0。
// dev=+3：C=108/P=95/F=1010/K=1000 → (108-95)-(1010-1000)=13-10=3。
func TestParityFormula(t *testing.T) {
	// dev=0：C-P=F-K
	r := computeParity(
		optBookSummary{MarkPrice: 105, UnderlyingPrice: 1},
		optBookSummary{MarkPrice: 95, UnderlyingPrice: 1},
		1010, // F
		1000, // K
		1,    // underlying
	)
	if r.Dev != 0 {
		t.Errorf("C=105 P=95 F=1010 K=1000: dev=%.4f, want 0", r.Dev)
	}
	// dev=+3：C=108/P=95/F=1010/K=1000 → (108-95)-(1010-1000)=13-10=3
	r2 := computeParity(
		optBookSummary{MarkPrice: 108, UnderlyingPrice: 1},
		optBookSummary{MarkPrice: 95, UnderlyingPrice: 1},
		1010, // F
		1000, // K
		1,    // underlying
	)
	if r2.Dev != 3 {
		t.Errorf("C=108 P=95 F=1010 K=1000: dev=%.4f, want 3", r2.Dev)
	}
	// dev_bps = dev/underlying×10000 = 3/1×10000 = 30000（underlying=1 是测试简化）
	if r2.DevBps != 30000 {
		t.Errorf("dev_bps=%.4f, want 30000 (dev=3/underlying=1×10000)", r2.DevBps)
	}
	// 真实场景：underlying~78000
	r3 := computeParity(
		optBookSummary{MarkPrice: 0.0013846, UnderlyingPrice: 78000}, // ~108 USD
		optBookSummary{MarkPrice: 0.0012179, UnderlyingPrice: 78000}, // ~95 USD
		78100, // F
		78000, // K
		78000, // underlying
	)
	// dev = (108-95)-(78100-78000) = 13-100 = -87 USD → dev_bps = -87/78000×10000 ≈ -11.15
	if r3.Dev >= 0 {
		t.Errorf("expected negative dev (F-K=100 > C-P=13), got %.4f", r3.Dev)
	}
}

// 用例②：流动性过滤
// bid=ask=0 的行权价跳过（per-contract 计数）；ATM±10% 外跳过（per-pair 计数）。
func TestLiquidityFilter(t *testing.T) {
	now := time.Now()
	// BTC 期货 F=78000（ATM=78000，±10% = 70200~85800）
	futures := []optBookSummary{
		{InstrumentName: "BTC-27SEP26", MarkPrice: 78000, BaseCurrency: "BTC"},
	}
	// 期权：strike=78000（ATM，在带内）+ strike=60000（OTM，带外）+ strike=80000（在带内但 bid=ask=0）
	options := []optBookSummary{
		// ATM call+put（有市价）
		{InstrumentName: "BTC-27SEP26-78000-C", MarkPrice: 0.001, UnderlyingPrice: 78000, BidPrice: 0.0009, AskPrice: 0.0011, BaseCurrency: "BTC"},
		{InstrumentName: "BTC-27SEP26-78000-P", MarkPrice: 0.001, UnderlyingPrice: 78000, BidPrice: 0.0009, AskPrice: 0.0011, BaseCurrency: "BTC"},
		// OTM call+put（strike=60000，带外 60000<70200）
		{InstrumentName: "BTC-27SEP26-60000-C", MarkPrice: 0.5, UnderlyingPrice: 78000, BidPrice: 0.49, AskPrice: 0.51, BaseCurrency: "BTC"},
		{InstrumentName: "BTC-27SEP26-60000-P", MarkPrice: 0.0001, UnderlyingPrice: 78000, BidPrice: 0.00005, AskPrice: 0.00015, BaseCurrency: "BTC"},
		// 无市价 call+put（bid=ask=0，strike=80000 在带内）
		{InstrumentName: "BTC-27SEP26-80000-C", MarkPrice: 0.0001, UnderlyingPrice: 78000, BidPrice: 0, AskPrice: 0, BaseCurrency: "BTC"},
		{InstrumentName: "BTC-27SEP26-80000-P", MarkPrice: 0.0001, UnderlyingPrice: 78000, BidPrice: 0, AskPrice: 0, BaseCurrency: "BTC"},
	}
	out := computeAllParity(ParityInput{
		Options: options, Futures: futures,
		Bases: []string{"BTC"}, ExpiryCount: 2,
	}, now)
	// ATM 78000 在带内 → 1 个 result
	// OTM 60000 带外 → OutOfATM=1（per-pair）
	// 80000 在带内（78000×1.1=85800>80000）但 bid=ask=0 → NoMkt=2（per-contract: call+put）
	if len(out.Results) != 1 {
		t.Errorf("want 1 result (ATM only), got %d (NoMkt=%d OutOfATM=%d)",
			len(out.Results), out.NoMkt, out.OutOfATM)
	}
	if out.NoMkt != 2 {
		t.Errorf("want NoMkt=2 (bid=ask=0 call+put), got %d", out.NoMkt)
	}
	if out.OutOfATM != 1 {
		t.Errorf("want OutOfATM=1 (OTM pair), got %d", out.OutOfATM)
	}
}

// 用例③：F 缺失（无同到期期货）→ 跳过+计数，不 panic。
func TestFutureMissing(t *testing.T) {
	now := time.Now()
	// 无期货数据
	options := []optBookSummary{
		{InstrumentName: "BTC-27SEP26-78000-C", MarkPrice: 0.001, UnderlyingPrice: 78000, BidPrice: 0.0009, AskPrice: 0.0011, BaseCurrency: "BTC"},
		{InstrumentName: "BTC-27SEP26-78000-P", MarkPrice: 0.001, UnderlyingPrice: 78000, BidPrice: 0.0009, AskPrice: 0.0011, BaseCurrency: "BTC"},
	}
	out := computeAllParity(ParityInput{
		Options: options, Futures: nil,
		Bases: []string{"BTC"}, ExpiryCount: 2,
	}, now)
	if len(out.Results) != 0 {
		t.Errorf("want 0 results (no future), got %d", len(out.Results))
	}
	if out.Skipped == 0 {
		t.Errorf("want Skipped>0 (F missing), got %d", out.Skipped)
	}
}

// 用例④：告警规则
// |dev|>15 连续 3 tick 触发 1 条；<15 不触发；持续超阈不重复刷（同 instrument 告警间隔 ≥1h）。
func TestAlertRule(t *testing.T) {
	var alerts []string
	alertFn := func(level, msg string) { alerts = append(alerts, level+":"+msg) }
	fixedNow := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	rule := NewAlertRule(15, alertFn)
	rule.Now = func() time.Time { return fixedNow }
	rule.RealertInterval = time.Hour

	inst := optInstrument{Base: "BTC", Expiry: "27SEP26", ExpiryYMD: "20260927", Strike: 78000, IsCall: true, Canonical: "BTC:20260927:78000"}
	// 构造 |dev_bps|=20（>15）的 result
	highDev := []ParityResult{{Instrument: inst, DevBps: 20, C: 100, P: 90, F: 78000, K: 78000}}
	lowDev := []ParityResult{{Instrument: inst, DevBps: 10, C: 100, P: 90, F: 78000, K: 78000}}

	// tick1: 超阈 → consecutive=1，不告警
	rule.Evaluate(highDev)
	if len(alerts) != 0 {
		t.Errorf("tick1: want 0 alerts (consecutive=1<3), got %d", len(alerts))
	}
	// tick2: 超阈 → consecutive=2，不告警
	rule.Evaluate(highDev)
	if len(alerts) != 0 {
		t.Errorf("tick2: want 0 alerts (consecutive=2<3), got %d", len(alerts))
	}
	// tick3: 超阈 → consecutive=3，告警 1 条
	rule.Evaluate(highDev)
	if len(alerts) != 1 {
		t.Fatalf("tick3: want 1 alert (consecutive=3), got %d", len(alerts))
	}
	if !strings.Contains(alerts[0], "info") {
		t.Errorf("tick3: want info level, got %s", alerts[0])
	}
	// tick4: 超阈 → consecutive 重置后=1，不告警（间隔内）
	rule.Evaluate(highDev)
	if len(alerts) != 1 {
		t.Errorf("tick4: want still 1 alert (realert interval), got %d", len(alerts))
	}
	// tick5: <15 → consecutive=0，不告警
	rule.Evaluate(lowDev)
	if len(alerts) != 1 {
		t.Errorf("tick5: want still 1 alert (below threshold), got %d", len(alerts))
	}
	// tick6/7/8: 再次连续 3 tick 超阈 → 但距上次告警 <1h → 不告警
	rule.Evaluate(highDev)
	rule.Evaluate(highDev)
	rule.Evaluate(highDev)
	if len(alerts) != 1 {
		t.Errorf("tick6-8: want still 1 alert (realert interval <1h), got %d", len(alerts))
	}
	// tick9: 推进时钟 >1h 后再连续 3 tick → 告警第 2 条
	fixedNow = fixedNow.Add(2 * time.Hour)
	rule.Now = func() time.Time { return fixedNow }
	rule.Reset()
	rule.Evaluate(highDev)
	rule.Evaluate(highDev)
	rule.Evaluate(highDev)
	if len(alerts) != 2 {
		t.Fatalf("tick9: want 2 alerts (after realert interval), got %d", len(alerts))
	}
}

// 用例⑤：facts 落库 schema 与现有 kind 约定一致。
func TestFactSchema(t *testing.T) {
	now := time.Now()
	futures := []optBookSummary{
		{InstrumentName: "BTC-27SEP26", MarkPrice: 78000, BaseCurrency: "BTC"},
	}
	options := []optBookSummary{
		{InstrumentName: "BTC-27SEP26-78000-C", MarkPrice: 0.001, UnderlyingPrice: 78000, BidPrice: 0.0009, AskPrice: 0.0011, BaseCurrency: "BTC"},
		{InstrumentName: "BTC-27SEP26-78000-P", MarkPrice: 0.001, UnderlyingPrice: 78000, BidPrice: 0.0009, AskPrice: 0.0011, BaseCurrency: "BTC"},
	}
	out := computeAllParity(ParityInput{
		Options: options, Futures: futures,
		Bases: []string{"BTC"}, ExpiryCount: 2,
	}, now)
	if len(out.Facts) == 0 {
		t.Fatal("want at least 1 fact, got 0")
	}
	f := out.Facts[0]
	if f.Kind != fact.KindOptParityDev {
		t.Errorf("Kind=%s, want %s", f.Kind, fact.KindOptParityDev)
	}
	if f.Unit != fact.UnitBps {
		t.Errorf("Unit=%s, want %s", f.Unit, fact.UnitBps)
	}
	if f.Venue != Venue {
		t.Errorf("Venue=%s, want %s", f.Venue, Venue)
	}
	if f.Src != "deribit_public" {
		t.Errorf("Src=%s, want deribit_public", f.Src)
	}
	// symbol 格式：BTC:YYYYMMDD:STRIKE（无 C/P 后缀——平价偏离是 call-put 对的属性）
	if !strings.Contains(f.Symbol, "BTC:") {
		t.Errorf("Symbol=%s, want BTC:YYYYMMDD:STRIKE format", f.Symbol)
	}
	if strings.HasSuffix(f.Symbol, ":C") || strings.HasSuffix(f.Symbol, ":P") {
		t.Errorf("Symbol=%s, must not have C/P suffix (parity is call-put pair attribute)", f.Symbol)
	}
	// Validate 不报错（kind 已注册）
	if err := f.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// 用例⑥（返修①）：到期日排序必须用 YYYYMMDD（字典序=日历序），不能用 DDMMMYY 字母序。
// DDMMMYY 字母序错乱：日份无前导零（4SEP26 排在 11SEP26 后）+ 月份字母序错（OCT26<SEP26）。
// 快照含 4 到期 [29AUG26,4SEP26,11SEP26,25SEP26]，ExpiryCount=2 → 正确取 [29AUG26,4SEP26]。
// 旧代码 DDMMMYY 排序=[11SEP26,25SEP26,29AUG26,4SEP26] → 取前 2=[11SEP26,25SEP26] → 红。
func TestExpirySortCalendar(t *testing.T) {
	now := time.Now()
	// 4 到期日，每个到期 1 个 call+put（strike=78000，ATM 带内）
	// + 每个到期 1 个期货（确保不因 F 缺失跳过）
	expiries := []string{"29AUG26", "4SEP26", "11SEP26", "25SEP26"}
	var futures []optBookSummary
	var options []optBookSummary
	for _, e := range expiries {
		futures = append(futures, optBookSummary{
			InstrumentName: "BTC-" + e, MarkPrice: 78000, BaseCurrency: "BTC",
		})
		options = append(options, optBookSummary{
			InstrumentName: "BTC-" + e + "-78000-C", MarkPrice: 0.001, UnderlyingPrice: 78000,
			BidPrice: 0.0009, AskPrice: 0.0011, BaseCurrency: "BTC",
		})
		options = append(options, optBookSummary{
			InstrumentName: "BTC-" + e + "-78000-P", MarkPrice: 0.001, UnderlyingPrice: 78000,
			BidPrice: 0.0009, AskPrice: 0.0011, BaseCurrency: "BTC",
		})
	}
	out := computeAllParity(ParityInput{
		Options: options, Futures: futures,
		Bases: []string{"BTC"}, ExpiryCount: 2,
	}, now)
	// 正确：取最近 2 个到期 = 29AUG26(20260829) + 4SEP26(20260904)
	// 旧代码 DDMMMYY 排序取 [11SEP26,25SEP26] → facts 含 SEP 月份 → 红
	if len(out.Facts) != 2 {
		t.Fatalf("want 2 facts (29AUG26+4SEP26), got %d", len(out.Facts))
	}
	for _, f := range out.Facts {
		// 29AUG26 → 20260829；4SEP26 → 20260904
		if !strings.Contains(f.Symbol, "20260829") && !strings.Contains(f.Symbol, "20260904") {
			t.Errorf("Symbol=%s, want only 29AUG26(20260829) or 4SEP26(20260904)", f.Symbol)
		}
	}
}
