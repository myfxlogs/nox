// d594_alias_test.go D-594 S4：XCU→COPPER 别名使 MT5 XCUUSD 与 CEX COPPERUSDT join 收敛。
package scan

import "testing"

func TestD594CopperAliasJoin(t *testing.T) {
	aliases := map[string]string{"XCU": "COPPER"}
	// MT5 侧：映射 canonical XCUUSD → join key XCU → 别名 COPPER。
	mt := applyAlias(NormalizeJoinKey("XCUUSD"), aliases)
	// CEX 侧：COPPERUSDT → cexCanonical COPPER → join key COPPER（别名不命中原样）。
	cex := applyAlias(NormalizeJoinKey(cexCanonical("COPPERUSDT")), aliases)
	if mt != "COPPER" || cex != "COPPER" {
		t.Fatalf("join 不收敛: mt=%q cex=%q, want COPPER/COPPER", mt, cex)
	}
	// cleanAliases 循环检测：XCU→COPPER 无环保留。
	out, dropped := cleanAliases(aliases)
	if out["XCU"] != "COPPER" || len(dropped) != 0 {
		t.Fatalf("cleanAliases: out=%v dropped=%v", out, dropped)
	}
}
