package main

import "testing"

// econValidateMatchSeg 键域漂移拦截断言（D-587④ 续段③）：match 段未知键=签名
// 永不命中且无告警，gen-econ 门禁单点拦。合法键/无段=过。
func TestEconValidateMatchSeg(t *testing.T) {
	ok := []string{
		"# 条目\n\n**判据挂接**：EA-05。\nmatch:\n  payoff_class: naked_short_vol\n",
		"# 条目\nmatch:\n  canonicals: [XAUUSD, PAXGUSDT]\n  venues: [binance, gate]\n",
		"# 无 match 段条目\n\n**判据挂接**：EA-02。\n正文。",
	}
	for i, c := range ok {
		if err := econValidateMatchSeg("ok.md", c); err != nil {
			t.Errorf("case %d 应过: %v", i, err)
		}
	}
	bad := []string{
		"# 条目\nmatch:\n  payoff: naked_short_vol\n",     // 键名漂移（payoff≠payoff_class）
		"# 条目\nmatch:\n  payoff_class: x\n  foo: bar\n", // 合法键混未知键
	}
	for i, c := range bad {
		if err := econValidateMatchSeg("bad.md", c); err == nil {
			t.Errorf("case %d 应拦（未知键）", i)
		}
	}
}
