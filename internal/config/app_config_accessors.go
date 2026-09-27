// app_config_accessors.go —— D-523 S1 拆出：键 accessor（set/get/typ 元数据构造器）。
package config

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AppCfgMeta 键元数据：typ=前端控件型（bool|number|string）+set=typed setter+get=字段格式化回读。
type AppCfgMeta struct {
	Typ string
	Set func(*Config, string) error
	Get func(*Config) string
}

func f64(g func(*Config) *float64) AppCfgMeta {
	return AppCfgMeta{"number",
		func(c *Config, v string) error {
			x, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return err
			}
			*g(c) = x
			return nil
		},
		func(c *Config) string { return strconv.FormatFloat(*g(c), 'g', -1, 64) }}
}
func intv(g func(*Config) *int) AppCfgMeta {
	return AppCfgMeta{"number",
		func(c *Config, v string) error {
			x, err := strconv.Atoi(v)
			if err != nil {
				return err
			}
			*g(c) = x
			return nil
		},
		func(c *Config) string { return strconv.Itoa(*g(c)) }}
}
func boolv(g func(*Config) *bool) AppCfgMeta {
	return AppCfgMeta{"bool",
		func(c *Config, v string) error {
			x, err := strconv.ParseBool(v)
			if err != nil {
				return err
			}
			*g(c) = x
			return nil
		},
		func(c *Config) string { return strconv.FormatBool(*g(c)) }}
}
func durv(g func(*Config) *time.Duration) AppCfgMeta {
	return AppCfgMeta{"string",
		func(c *Config, v string) error {
			x, err := time.ParseDuration(v)
			if err != nil {
				return err
			}
			*g(c) = x
			return nil
		},
		func(c *Config) string { return g(c).String() }}
}
func strv(g func(*Config) *string) AppCfgMeta {
	return AppCfgMeta{"string",
		func(c *Config, v string) error { *g(c) = v; return nil },
		func(c *Config) string { return *g(c) }}
}
func listv(g func(*Config) *[]string) AppCfgMeta {
	return AppCfgMeta{"string",
		func(c *Config, v string) error { *g(c) = parseCSV(v); return nil },
		func(c *Config) string { return strings.Join(*g(c), ",") }}
}
func setv(g func(*Config) *map[string]bool) AppCfgMeta {
	return AppCfgMeta{"string",
		func(c *Config, v string) error { *g(c) = parseSet(v); return nil },
		func(c *Config) string {
			ks := make([]string, 0, len(*g(c)))
			for k := range *g(c) {
				ks = append(ks, k)
			}
			sort.Strings(ks)
			return strings.Join(ks, ",")
		}}
}
func fmapv(g func(*Config) *map[string]float64) AppCfgMeta {
	return AppCfgMeta{"string",
		func(c *Config, v string) error { *g(c) = parseFloatMap(v); return nil },
		func(c *Config) string {
			ks := make([]string, 0, len(*g(c)))
			for k := range *g(c) {
				ks = append(ks, k)
			}
			sort.Strings(ks)
			var sb strings.Builder
			for i, k := range ks {
				if i > 0 {
					sb.WriteByte(',')
				}
				fmt.Fprintf(&sb, "%s:%s", k, strconv.FormatFloat((*g(c))[k], 'g', -1, 64))
			}
			return sb.String()
		}}
}
func takerG(venue string) AppCfgMeta { // ARB_COMMISSION_TAKER_*：venue 键位映射特例
	return AppCfgMeta{"number",
		func(c *Config, v string) error {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return err
			}
			if c.CommissionTaker == nil {
				c.CommissionTaker = map[string]float64{}
			}
			c.CommissionTaker[venue] = f
			return nil
		},
		func(c *Config) string { return strconv.FormatFloat(c.CommissionTaker[venue], 'g', -1, 64) }}
}
