// app_config_overlay.go —— D-368 S3-S5：app_config KV overlay（boot 时 DB>env>default）。
// 显式键白名单注册表（防注入面——非白名单键 warn 忽略）；typed setter 解析失败 warn+保留
// env/默认（坏 DB 值不置零不致命）。无热更新 v1：必须在 engine/组件构造前调用（main.go
// config.Load() 之后一行），顺序后移会静默失效。
package config

import (
	"log"
	"os"
	"sort"
	"strconv"
)

// appConfigKeys D-368 Bucket B 白名单（八键；秘密/安全开关键刻意不注册——§1 分桶）。
// value=typed setter；非法 DB 值 → setter 返回 err → warn+保留 env/默认。
var appConfigKeys = map[string]func(*Config, string) error{
	"ARB_OPP_NOTIONAL_USD": func(c *Config, v string) error {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return err
		}
		c.OpportunityNotionalUSD = f
		return nil
	},
	"ARB_AUTOTRADE_MAX_NOTIONAL": func(c *Config, v string) error {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return err
		}
		c.AutoTradeMaxNotional = f
		return nil
	},
	"ARB_AUTOTRADE_MAX_POSITIONS": func(c *Config, v string) error {
		n, err := strconv.Atoi(v)
		if err != nil {
			return err
		}
		c.AutoTradeMaxPositions = n
		return nil
	},
	"ARB_AUTOTRADE_MIN_NET_SWAP_BPS": func(c *Config, v string) error {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return err
		}
		c.AutoTradeMinNetSwapBps = f
		return nil
	},
	"ARB_CARRY_REGIME_FLOOR_PCT": func(c *Config, v string) error {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return err
		}
		c.CarryRegimeFloorPct = f
		return nil
	},
	"ARB_FUNDING_MAX_BREAKEVEN_DAYS": func(c *Config, v string) error {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return err
		}
		c.FundingMaxBreakevenDays = f
		return nil
	},
	"ARB_CROSS_MIN_GROSS_BPS_CFD": func(c *Config, v string) error {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return err
		}
		c.CrossVenueMinGrossBpsCFD = f
		return nil
	},
	"ARB_CLOSE_BPS_THR": func(c *Config, v string) error {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return err
		}
		c.CloseBpsThr = f
		return nil
	},
}

// ApplyAppConfigOverlay DB 行覆盖 cfg（boot overlay：DB>env>default）。
// 白名单键命中 → typed set + S4 env 残值告警 + S5 source 审计行；DB 无行 → env fallback
// 静默（过渡期合法路径，审计行仍打 source=env/default）；非白名单键 → warn 忽略。
// keys 遍历按字典序（日志确定性）。
func ApplyAppConfigOverlay(cfg *Config, rows map[string]string) {
	keys := make([]string, 0, len(rows))
	seen := map[string]bool{}
	for k := range rows {
		keys = append(keys, k)
		seen[k] = true
	}
	sort.Strings(keys)
	for _, k := range keys {
		setter, ok := appConfigKeys[k]
		if !ok {
			log.Printf("[config] app_config key %s not whitelisted, ignored", k)
			continue
		}
		v := rows[k]
		if err := setter(cfg, v); err != nil {
			log.Printf("[config] %s: invalid app_config value %q ignored (env/default kept): %v", k, v, err)
			continue
		}
		if envV := os.Getenv(k); envV != "" && envV != v {
			log.Printf("[config] %s: env value ignored, app_config row wins", k)
		}
		log.Printf("[config] %s=%s (source=app_config)", k, v)
	}
	// S5 审计行：无 DB 行的键也打 source（env/default），运维一眼看清每个旋钮谁在供值。
	var noRow []string
	for k := range appConfigKeys {
		if !seen[k] {
			noRow = append(noRow, k)
		}
	}
	sort.Strings(noRow)
	for _, k := range noRow {
		source := "default"
		if os.Getenv(k) != "" {
			source = "env"
		}
		log.Printf("[config] %s (source=%s)", k, source)
	}
}
