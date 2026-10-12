// d403r2_pipeline_race_test.go —— D-403 R2 race 锚：pipeline 白名单热刷新写侧（Reload/Set）
// vs 执行读侧（xassetAllowSpecs+Match）并发，-race 下零 DATA RACE 即过（机检外层 ×3）。
package exec

import (
	"fmt"
	"sync"
	"testing"
)

func TestD403R2_PipelineAllowSnapshot_Race(t *testing.T) {
	p := NewPipeline(NewRegistry(), nil, nil, RiskLimits{}, "real")
	p.SetXAssetComboAllow(nil) // 初始空集全拒
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // 写侧：模拟 approve 扇出（整体替换快照）
		defer wg.Done()
		for i := 0; i < 100; i++ {
			p.ReloadXAssetComboAllow([]XAssetComboAllowSpec{{
				CanonicalBuy: "BTCUSDT", CanonicalSell: "BTCUSD", VenueBuy: "gate:perp", VenueSell: "1279967:mt5",
				MaxLongQty: 1, MaxShortQty: 1, MaxGrossUSD: 10,
			}})
		}
	}()
	go func() { // 读侧：模拟执行闸 checkXAssetComboAllow 的读点
		defer wg.Done()
		for i := 0; i < 500; i++ {
			_ = MatchXAssetComboAllow(p.xassetAllowSpecs(), "BTCUSDT", "BTCUSD", "gate:perp", "1279967:mt5")
		}
	}()
	wg.Wait()
}

func TestD403R2_PipelineAllowSnapshot_NilDeny(t *testing.T) {
	p := NewPipeline(NewRegistry(), nil, nil, RiskLimits{}, "real")
	if p.xassetAllowSpecs() != nil {
		t.Fatal("未接线应等价 nil slice")
	}
	if MatchXAssetComboAllow(p.xassetAllowSpecs(), "A", "B", "va", "vb") != nil {
		t.Fatal("nil 快照=空集全拒（零信任语义不破）")
	}
	if fmt.Sprint(p.XAssetAllowSpecs()) != "[]" && p.XAssetAllowSpecs() != nil {
		t.Fatal("公开读口语义应一致")
	}
}
