package dashboard

import (
	"testing"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/store/pgstore"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestBuildMatrixUniverse_DedupAndGroupOrder(t *testing.T) {
	accounts := []*v1.BrokerAccount{
		{Venue: "v1", Status: "connected"},
		{Venue: "v2", Status: "connected"},
	}
	groups := map[string][]SymbolGroup{
		"v1": {
			{Symbol: "EURUSD", Group: "Forex", ContractSize: 100000},
			{Symbol: "GOLD", Group: "Metals", ContractSize: 100},
		},
		"v2": {
			{Symbol: "EURUSD", Group: "Forex", ContractSize: 100000},
			{Symbol: "EURUSDm", Group: "Forex", ContractSize: 100000},
		},
	}
	u := buildMatrixUniverse(accounts, groups, nil, nil)
	if len(u.Symbols) != 3 {
		t.Fatalf("want 3 rows, got %d", len(u.Symbols))
	}
	if u.Symbols[0].Symbol != "EURUSD" || u.Symbols[0].Group != "Forex" {
		t.Errorf("row0 want EURUSD/Forex, got %s/%s", u.Symbols[0].Symbol, u.Symbols[0].Group)
	}
	if u.Symbols[0].CoverageTotal != 2 {
		t.Errorf("EURUSD coverage total want 2, got %d", u.Symbols[0].CoverageTotal)
	}
	if len(u.Symbols[0].Accounts) != 2 {
		t.Errorf("EURUSD accounts want 2, got %d", len(u.Symbols[0].Accounts))
	}
	// 组并集按账号序首现：v1 Forex 先，v1 Metals 次之，v2 Forex 与已有 Forex 重复不追加
	wantGroups := []string{"Forex", "Metals"}
	if len(u.Groups) != 2 {
		t.Fatalf("groups want %v, got %v", wantGroups, u.Groups)
	}
	for i, g := range wantGroups {
		if u.Groups[i] != g {
			t.Errorf("groups[%d] want %s, got %s", i, g, u.Groups[i])
		}
	}
}

func TestBuildMatrixUniverse_States(t *testing.T) {
	accounts := []*v1.BrokerAccount{
		{Venue: "v1", Status: "connected"},
		{Venue: "v2", Status: "connected"},
	}
	groups := map[string][]SymbolGroup{
		"v1": {{Symbol: "S1", Group: "Forex", ContractSize: 100}},
		"v2": {{Symbol: "S1", Group: "Forex", ContractSize: 100}},
	}
	// complete: 两个都映射
	mappings := []pgstore.SymbolMapping{
		{Venue: "v1", BrokerSymbol: "S1", Canonical: "X"},
		{Venue: "v2", BrokerSymbol: "S1", Canonical: "X"},
	}
	u := buildMatrixUniverse(accounts, groups, mappings, nil)
	row := u.Symbols[0]
	if row.State != v1.MatrixState_MATRIX_STATE_COMPLETE {
		t.Errorf("complete state want COMPLETE, got %v", row.State)
	}
	if row.CoverageMapped != 2 || row.CoverageTotal != 2 {
		t.Errorf("complete coverage want 2/2, got %d/%d", row.CoverageMapped, row.CoverageTotal)
	}

	// partial
	mappings = []pgstore.SymbolMapping{
		{Venue: "v1", BrokerSymbol: "S1", Canonical: "X"},
	}
	u = buildMatrixUniverse(accounts, groups, mappings, nil)
	row = u.Symbols[0]
	if row.State != v1.MatrixState_MATRIX_STATE_PARTIAL {
		t.Errorf("partial state want PARTIAL, got %v", row.State)
	}

	// missing
	mappings = nil
	u = buildMatrixUniverse(accounts, groups, mappings, nil)
	row = u.Symbols[0]
	if row.State != v1.MatrixState_MATRIX_STATE_MISSING {
		t.Errorf("missing state want MISSING, got %v", row.State)
	}
}

func TestBuildMatrixUniverse_Conflict(t *testing.T) {
	accounts := []*v1.BrokerAccount{
		{Venue: "v1", Status: "connected"},
		{Venue: "v2", Status: "connected"},
	}
	groups := map[string][]SymbolGroup{
		"v1": {{Symbol: "GOLD", Group: "Metals", ContractSize: 100}},
		"v2": {{Symbol: "GOLD", Group: "Metals", ContractSize: 100}},
	}
	mappings := []pgstore.SymbolMapping{
		{Venue: "v1", BrokerSymbol: "GOLD", Canonical: "XAUUSD"},
		{Venue: "v2", BrokerSymbol: "GOLD", Canonical: "GOLD"},
	}
	u := buildMatrixUniverse(accounts, groups, mappings, nil)
	row := u.Symbols[0]
	if len(row.MappedCanonicals) != 2 {
		t.Errorf("conflict mapped canonicals want 2, got %d", len(row.MappedCanonicals))
	}
	if row.State != v1.MatrixState_MATRIX_STATE_COMPLETE {
		t.Errorf("conflict with all mapped should be COMPLETE, got %v", row.State)
	}
}

func TestBuildMatrixUniverse_CanonicalMerge(t *testing.T) {
	// 同商品跨 broker 原生符号不同：XAUUSDm(Exness) + GOLD(FxPro) → 归并成一行 XAUUSD
	accounts := []*v1.BrokerAccount{
		{Venue: "v1", Status: "connected"},
		{Venue: "v2", Status: "connected"},
	}
	groups := map[string][]SymbolGroup{
		"v1": {{Symbol: "XAUUSDm", Group: "Forex", ContractSize: 100}},
		"v2": {{Symbol: "GOLD", Group: "Metals", ContractSize: 100}},
	}
	mappings := []pgstore.SymbolMapping{
		{Venue: "v1", BrokerSymbol: "XAUUSDm", Canonical: "XAUUSD"},
		{Venue: "v2", BrokerSymbol: "GOLD", Canonical: "XAUUSD"},
	}
	u := buildMatrixUniverse(accounts, groups, mappings, nil)
	if len(u.Symbols) != 1 {
		t.Fatalf("want 1 merged row, got %d", len(u.Symbols))
	}
	row := u.Symbols[0]
	if row.Symbol != "XAUUSD" {
		t.Errorf("merged symbol want XAUUSD, got %s", row.Symbol)
	}
	if row.Group != "" {
		t.Errorf("merged group want \"\" (跨组), got %q", row.Group)
	}
	if row.CoverageTotal != 2 || row.CoverageMapped != 2 {
		t.Errorf("merged coverage want 2/2, got %d/%d", row.CoverageMapped, row.CoverageTotal)
	}
	if row.State != v1.MatrixState_MATRIX_STATE_COMPLETE {
		t.Errorf("merged state want COMPLETE, got %v", row.State)
	}
	if len(row.Accounts) != 2 {
		t.Fatalf("merged accounts want 2, got %d", len(row.Accounts))
	}
	rawByVenue := map[string]string{}
	groupByVenue := map[string]string{}
	for _, a := range row.Accounts {
		if a.Canonical == nil || *a.Canonical != "XAUUSD" {
			t.Errorf("account %s canonical want XAUUSD, got %v", a.Venue, a.Canonical)
		}
		if a.RawSymbol == "" {
			t.Errorf("account %s rawSymbol empty", a.Venue)
		}
		rawByVenue[a.Venue] = a.RawSymbol
		groupByVenue[a.Venue] = a.Group
	}
	if rawByVenue["v1"] != "XAUUSDm" || rawByVenue["v2"] != "GOLD" {
		t.Errorf("per-venue raw symbols want v1=XAUUSDm v2=GOLD, got %v", rawByVenue)
	}
	if groupByVenue["v1"] != "Forex" || groupByVenue["v2"] != "Metals" {
		t.Errorf("per-venue native groups want v1=Forex v2=Metals, got %v", groupByVenue)
	}
	if rawByVenue["v1"] != "XAUUSDm" || rawByVenue["v2"] != "GOLD" {
		t.Errorf("per-venue raw symbols want v1=XAUUSDm v2=GOLD, got %v", rawByVenue)
	}
}

func TestBuildMatrixUniverse_CanonicalSingleMemberKeepsGroup(t *testing.T) {
	// 唯一已配齐行：展示名改 canonical，但分组保留（EURUSD 仍属 Forex）
	accounts := []*v1.BrokerAccount{
		{Venue: "v1", Status: "connected"},
	}
	groups := map[string][]SymbolGroup{
		"v1": {{Symbol: "EURUSD", Group: "Forex", ContractSize: 100000}},
	}
	mappings := []pgstore.SymbolMapping{
		{Venue: "v1", BrokerSymbol: "EURUSD", Canonical: "EURUSD"},
	}
	u := buildMatrixUniverse(accounts, groups, mappings, nil)
	if len(u.Symbols) != 1 {
		t.Fatalf("want 1 row, got %d", len(u.Symbols))
	}
	row := u.Symbols[0]
	if row.Symbol != "EURUSD" {
		t.Errorf("symbol want EURUSD, got %s", row.Symbol)
	}
	if row.Group != "Forex" {
		t.Errorf("group want Forex, got %q", row.Group)
	}
	if row.State != v1.MatrixState_MATRIX_STATE_COMPLETE {
		t.Errorf("state want COMPLETE, got %v", row.State)
	}
	if len(row.Accounts) != 1 || row.Accounts[0].RawSymbol != "EURUSD" {
		t.Errorf("account rawSymbol want EURUSD, got %v", row.Accounts)
	}
}

func TestBuildMatrixUniverse_StoppedExcluded(t *testing.T) {
	accounts := []*v1.BrokerAccount{
		{Venue: "v1", Status: "connected"},
		{Venue: "v2", Status: "stopped"},
	}
	groups := map[string][]SymbolGroup{
		"v1": {{Symbol: "EURUSD", Group: "Forex", ContractSize: 100000}},
	}
	mappings := []pgstore.SymbolMapping{
		{Venue: "v1", BrokerSymbol: "EURUSD", Canonical: "EURUSD"},
	}
	u := buildMatrixUniverse(accounts, groups, mappings, nil)
	row := u.Symbols[0]
	if row.CoverageTotal != 1 {
		t.Errorf("stopped excluded, coverage total want 1, got %d", row.CoverageTotal)
	}
	if len(u.Accounts) != 2 {
		t.Errorf("accounts list must still include stopped, got %d", len(u.Accounts))
	}
	stat := u.GroupStats[0]
	if stat.Complete != 1 {
		t.Errorf("group complete count want 1, got %d", stat.Complete)
	}
}

func TestBuildMatrixUniverse_ContractRange(t *testing.T) {
	accounts := []*v1.BrokerAccount{
		{Venue: "v1", Status: "connected"},
		{Venue: "v2", Status: "connected"},
	}
	groups := map[string][]SymbolGroup{
		"v1": {{Symbol: "XAUUSD", Group: "Metals", ContractSize: 100}},
		"v2": {{Symbol: "XAUUSD", Group: "Metals", ContractSize: 1000}},
	}
	u := buildMatrixUniverse(accounts, groups, nil, nil)
	row := u.Symbols[0]
	if row.ContractMin != 100 || row.ContractMax != 1000 {
		t.Errorf("contract range want 100-1000, got %v-%v", row.ContractMin, row.ContractMax)
	}
}

func TestBuildMatrixUniverse_Canonicals(t *testing.T) {
	accounts := []*v1.BrokerAccount{
		{Venue: "v1", Status: "connected"},
	}
	groups := map[string][]SymbolGroup{
		"v1": {{Symbol: "EURUSD", Group: "Forex", ContractSize: 100000}},
	}
	u := buildMatrixUniverse(accounts, groups, nil, []string{"EURUSD", "GBPUSD"})
	if len(u.Canonicals) != 2 || u.Canonicals[0] != "EURUSD" || u.Canonicals[1] != "GBPUSD" {
		t.Errorf("canonicals pass-through want [EURUSD GBPUSD], got %v", u.Canonicals)
	}
}

func TestBuildMatrixUniverse_AccountsPBPassThrough(t *testing.T) {
	accounts := []*v1.BrokerAccount{
		{Venue: "v1", Status: "connected", CreatedAt: timestamppb.Now()},
	}
	u := buildMatrixUniverse(accounts, nil, nil, nil)
	if len(u.Accounts) != 1 || u.Accounts[0].Venue != "v1" {
		t.Errorf("accounts PB passthrough failed: %v", u.Accounts)
	}
}

// D-065 裁剪：有 canonicals 时，不匹配的 symbol（如 Stock CFD）被裁掉。
func TestBuildMatrixUniverse_PruneUnmatched(t *testing.T) {
	accounts := []*v1.BrokerAccount{
		{Venue: "v1", Status: "connected"},
	}
	groups := map[string][]SymbolGroup{
		"v1": {
			{Symbol: "EURUSD", Group: "Forex", ContractSize: 100000},
			{Symbol: "EURUSDm", Group: "Forex", ContractSize: 100000},
			{Symbol: "JPMm", Group: "Stock CFD's", ContractSize: 1},
			{Symbol: "AAPL", Group: "Stock CFD's", ContractSize: 1},
		},
	}
	// canonicals 含 EURUSD → EURUSD/EURUSDm 保留，JPMm/AAPL 裁掉
	u := buildMatrixUniverse(accounts, groups, nil, []string{"EURUSD"})
	if len(u.Symbols) != 2 {
		t.Fatalf("prune want 2 rows (EURUSD+EURUSDm), got %d", len(u.Symbols))
	}
	for _, s := range u.Symbols {
		if s.Symbol == "JPMm" || s.Symbol == "AAPL" {
			t.Errorf("Stock CFD %s should be pruned", s.Symbol)
		}
	}
}
