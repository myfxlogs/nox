// app_secret_descs.go —— D-545 S3：密钥键中文一句话说明（设置页密钥卡主行单源）。
// 仿 app_config_descs.go 形态；逐键按消费点写实语义，key/desc 原文保留英文键名。
package dashboard

var appSecretDescs = map[string]string{
	"ARB_FEISHU_APP_ID":       "飞书自建应用 App ID（报告/告警推送通道凭据）",
	"ARB_FEISHU_APP_SECRET":   "飞书自建应用 App Secret（报告/告警推送通道凭据）",
	"ARB_FEISHU_VERIFY_TOKEN": "飞书事件订阅 Verify Token（事件回调验签）",
	"ARB_AI_BASE_URL":         "AI 分析 OpenAI 兼容端点地址（复盘/分析师调用）",
	"ARB_AI_API_KEY":          "AI 分析 API Key（复盘/分析师调用凭据）",
	"ARB_AI_MODEL":            "AI 分析模型名（复盘/分析师调用选型）",
	"ARB_AI_PG_DSN":           "AI 侧库连接串（缺则 AI 库写入关闭）",
	"LONGBRIDGE_APP_KEY":      "Longbridge OpenAPI App Key（港美股行情凭据）",
	"LONGBRIDGE_APP_SECRET":   "Longbridge OpenAPI App Secret（港美股行情凭据）",
	"LONGBRIDGE_ACCESS_TOKEN": "Longbridge OpenAPI Access Token（港美股行情凭据）",
	"CMC_EMAIL":               "CoinMarketCal 账号邮箱（财历事件采集登录）",
	"CMC_PASSWORD":            "CoinMarketCal 账号密码（财历事件采集登录）",
	"ARB_CMC_CREATE_TPL":      "CoinMarketCal 建事件模板（财历事件提交格式）",
	"ARB_FRED_API_KEY":        "FRED 宏观数据 API Key（缺则该路采集跳过）",
	"TRX_VOTE_PRIVATE_KEY":    "TRX 质押/投票签名私钥（链上投票执行）",
	"TRONGRID_API_KEY":        "TronGrid API Key（TRX 链上采集限速提额）",
}

// appSecretGroup 密钥键分组（密钥卡列展示；按消费域归类）。
func appSecretGroup(k string) string {
	switch {
	case k == "ARB_FRED_API_KEY" || k == "CMC_EMAIL" || k == "CMC_PASSWORD" || k == "ARB_CMC_CREATE_TPL":
		return "宏观采集"
	case k == "TRONGRID_API_KEY" || k == "TRX_VOTE_PRIVATE_KEY":
		return "TRX 链上"
	case len(k) >= 11 && k[:11] == "ARB_FEISHU_":
		return "推送通道"
	case len(k) >= 7 && k[:7] == "ARB_AI_":
		return "AI 分析"
	default:
		return "行情接入" // LONGBRIDGE_*3
	}
}
