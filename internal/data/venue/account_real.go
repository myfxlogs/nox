package venue

// account_real.go —— 账户行口径纯函数（D-360/D-361/D-363；3d-P1#1c 自 cexscan 下沉，
// cexscan 写侧与 dashboard 实时态读侧共用单源，禁复制）。

// IsRealAccount D-360 S1：real 判定纯函数（证真才留）。
// cex → !Simulated（原生键，v2 核证推翻「构造假设」）；cfd → venue∈broker_accounts.is_real
// 键集（核证点②：snapshot.venue ≡ user_id::text，生产 DB 两列互证留证 9-20）；
// 未知 AssetType → 丢。realKeys nil 时 cfd 一律丢（fail-closed）。
func IsRealAccount(assetType string, simulated bool, venue string, realKeys map[string]bool) bool {
	switch assetType {
	case "cex":
		return !simulated
	case "cfd":
		return realKeys[venue]
	default:
		return false
	}
}

// isUnifiedAccountVenue D-361 S3：统一账户 venue 白名单（仅 okx——统一账户 spot/perp 双
// config 查同一 /account/balance；binance/gate 现货合约分钱包实证不归一，SSOT §0）。
func isUnifiedAccountVenue(venue string) bool { return venue == "okx" }

// NormalizeVenueType D-361 S3/D-363-R1：统一账户写侧归一——仅 okx **交易账户**
// （spot/perp，双 config 同钱）归一 "unified"；D-363 子账户（funding/earn）是不同钱
// （各自独立端点独立余额），原样穿透——归一无条件压全部 type 会把 earn $5,261 塌进
// unified（生产 23:06 实证顶掉交易账户行）。读侧零改。
func NormalizeVenueType(assetType, venue, venueType string) string {
	if assetType == "cex" && isUnifiedAccountVenue(venue) && (venueType == "spot" || venueType == "perp") {
		return "unified"
	}
	return venueType
}
