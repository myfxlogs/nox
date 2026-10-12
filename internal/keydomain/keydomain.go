// Package keydomain D-496：键域宪法单源——venue 键域解析的唯一实现。
// 宪法（migration 0142 在库内强制）：R1 venue=裸端点（MT=账号号 / CEX=交易所名，
// 禁含 ':'）；R2 venue_type∈{mt4,mt5,perp,spot,funding,earn,option,unified,”=非通道}；
// R3 venue_key=venue:venue_type 由 DB GENERATED 列产出，代码侧复合键的拆分只允许经
// 本包 Parse，禁止业务层手写 SplitN/TrimSuffix/HasPrefix（历史 132 处收编归一）。
package keydomain

import "strings"

// VenueKey 通道身份（venue, venue_type 两列自然键的内存投影）。
type VenueKey struct {
	Venue string // R1 裸端点
	Type  string // R2 通道型；''=非通道/未知
}

// Parse 复合键→裸键+类型。无 ':' → Type=”（裸值原样）。
func Parse(key string) VenueKey {
	v, t, _ := strings.Cut(key, ":")
	return VenueKey{Venue: v, Type: t}
}

// New 构造 venue_key 字符串的等价物（String()）。Type=” → 返回裸 venue（与 DB
// generated 列 CASE 语义一致，不产生 'x:' 尾挂冒号）。
func New(venue, venueType string) VenueKey { return VenueKey{Venue: venue, Type: venueType} }

// String 复合键投影（与 venue_key 生成列同值）。
func (k VenueKey) String() string {
	if k.Type == "" {
		return k.Venue
	}
	return k.Venue + ":" + k.Type
}

// Bare 裸端点（ticks/facts/broker_symbol_map 等 R1 域列的查询键）。
func (k VenueKey) Bare() string { return k.Venue }

// IsCex :perp/:spot 通道（原 bus.IsCexVenueKey 判定域）。
func (k VenueKey) IsCex() bool { return k.Type == "perp" || k.Type == "spot" }

// IsMT :mt4/:mt5 通道。
func (k VenueKey) IsMT() bool { return k.Type == "mt4" || k.Type == "mt5" }

// ---- 函数式便捷口（手写 SplitN/TrimSuffix/HasSuffix 收编目标）----

// BareOf 复合键→裸端点（等价 strings.Split(key,":")[0]）。
func BareOf(key string) string { return Parse(key).Venue }

// TypeOf 复合键→通道型（无 ':' → ”）。
func TypeOf(key string) string { return Parse(key).Type }

// Join 裸端点+型→复合键（等价 venue+":"+type；type=” 不产生尾挂冒号）。
func Join(venue, venueType string) string { return New(venue, venueType).String() }

// IsType 复合键是否该通道型（等价 strings.HasSuffix(key, ":"+t)）。
func IsType(key, venueType string) bool { return Parse(key).Type == venueType }

// IsMTKey 复合键是否 MT 通道（:mt4/:mt5）。
func IsMTKey(key string) bool { return Parse(key).IsMT() }

// IsCexKey 复合键是否 CEX 通道（:perp/:spot）。
func IsCexKey(key string) bool { return Parse(key).IsCex() }

// NativeSymbol 原生品种名（XAUUSDm / XAU-USDT-SWAP；R4）。
type NativeSymbol string

// Canonical 归一品种名（XAUUSD / XAUUSDT；只经 broker_symbol_map/归一函数产出）。
type Canonical string
