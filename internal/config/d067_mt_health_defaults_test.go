// d067_mt_health_defaults_test.go —— D-068 锚：D-067 margin 独立阈值缺省
// （MARGIN_WARN=24h / MARGIN_CRIT=72h，init_margin_rate 准静态、重连驱动写入），
// swap facts 阈值不回退（FACT_WARN=2h / FACT_CRIT=6h）。
package config

import (
	"testing"
	"time"
)

// TestD067_MarginThresholdDefaults 清掉 margin env 后缺省值断言（t.Setenv 空串=缺省，
// 自动还原；envDur 解析失败回落 def）。
func TestD067_MarginThresholdDefaults(t *testing.T) {
	t.Setenv("ARB_MT_HEALTH_MARGIN_WARN", "")
	t.Setenv("ARB_MT_HEALTH_MARGIN_CRIT", "")
	t.Setenv("ARB_MT_HEALTH_FACT_WARN", "")
	t.Setenv("ARB_MT_HEALTH_FACT_CRIT", "")

	c := loadMTData()
	if c.HealthMarginWarn != 24*time.Hour {
		t.Errorf("HealthMarginWarn = %v, want 24h", c.HealthMarginWarn)
	}
	if c.HealthMarginCrit != 72*time.Hour {
		t.Errorf("HealthMarginCrit = %v, want 72h", c.HealthMarginCrit)
	}
	if c.HealthFactWarn != 2*time.Hour {
		t.Errorf("HealthFactWarn = %v, want 2h（swap 阈值不回退）", c.HealthFactWarn)
	}
	if c.HealthFactCrit != 6*time.Hour {
		t.Errorf("HealthFactCrit = %v, want 6h（swap 阈值不回退）", c.HealthFactCrit)
	}
}
