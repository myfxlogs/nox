// Package decimalutil 提供 float64 → decimal 安全转换（D-077 修复⑩ G5）。
//
// 约束（AGENTS.md §7 精度）：禁止 decimal.NewFromFloat() 直接调用（float64 二进制不精确）。
// 本包经 strconv 字符串中介：float64 → FormatFloat（十进制最短表示）→ decimal.NewFromString。
package decimalutil

import (
	"strconv"

	"github.com/shopspring/decimal"
)

// FromFloat64 经 strconv 字符串中介将 float64 转为 decimal.Decimal。
// 避免 decimal.NewFromFloat(f) 的二进制精度问题（如 1.05 → 1.050000000000000044...）。
// 返回错误仅当 FormatFloat 失败（极罕见，f 为 NaN/Inf 时）。
func FromFloat64(f float64) (decimal.Decimal, error) {
	s := strconv.FormatFloat(f, 'f', -1, 64) // -1 = 最短精确表示
	return decimal.NewFromString(s)
}

// MustFromFloat64 同 FromFloat64 但 panic on error（仅用于测试/常量，生产用 FromFloat64）。
func MustFromFloat64(f float64) decimal.Decimal {
	d, err := FromFloat64(f)
	if err != nil {
		panic(err)
	}
	return d
}
