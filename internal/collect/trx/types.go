// D-211 TRX 采集器 JSON 类型定义（TronGrid API 响应结构）。
package trx

import "encoding/json"

// accountJSON 对应 getaccount 响应（节选监控字段）。
type accountJSON struct {
	Address         string              `json:"address"`
	Balance         int64               `json:"balance"`
	Allowance       int64               `json:"allowance"`
	Votes           []voteJSON          `json:"votes"`
	FrozenV2        []frozenV2JSON      `json:"frozenV2"`
	AccountResource accountResourceJSON `json:"account_resource"`
}

type voteJSON struct {
	VoteAddress string `json:"vote_address"`
	VoteCount   int64  `json:"vote_count"`
}

type frozenV2JSON struct {
	Amount *int64  `json:"amount"` // nil = 该类型无自押（能量全委托时 ENERGY amount 空）
	Type   *string `json:"type"`   // nil 或 "" = BANDWIDTH（D-210 实证首项无 type）
}

type accountResourceJSON struct {
	DelegatedFrozenV2BalanceForEnergy int64 `json:"delegated_frozenV2_balance_for_energy"`
}

// resourceJSON 对应 getaccountresource 响应（节选）。
// D-278 S5：补 NetUsed/FreeNetUsed/EnergyLimit/EnergyUsed（剩余带宽/能量显示）。
type resourceJSON struct {
	NetLimit       int64 `json:"NetLimit"`
	NetUsed        int64 `json:"NetUsed"`
	FreeNetLimit   int64 `json:"freeNetLimit"`
	FreeNetUsed    int64 `json:"freeNetUsed"`
	EnergyLimit    int64 `json:"EnergyLimit"`
	EnergyUsed     int64 `json:"EnergyUsed"`
	TronPowerLimit int64 `json:"tronPowerLimit"`
}

// txsResponseJSON 对应 /v1/accounts/{addr}/transactions 响应。
type txsResponseJSON struct {
	Data []txJSON `json:"data"`
	Meta struct {
		Fingerprint string `json:"fingerprint"`
	} `json:"meta"`
}

// txJSON 对应单笔交易。TronGrid /v1/accounts/{addr}/transactions 响应结构（R1 对拍真实样本）：
// blockTimestamp(ms 驼峰)、transaction_id、raw_data.contract[].type + parameter.value
type txJSON struct {
	BlockTimestamp int64               `json:"block_timestamp"` // R1：真实响应是驼峰 blockTimestamp（非 block_timestamp）
	TxID           string              `json:"txID"`            // 止血修正：真实字段=txID（2026-09-03 实拍铁证，transaction_id/tx_id 均错）
	RawData        rawDataContractJSON `json:"raw_data"`
	Ret            []retJSON           `json:"ret"` // D-279：fee 捕获
}

// retJSON 对应 TronGrid ret 数组单行。
type retJSON struct {
	ContractRet string `json:"contractRet"`
	Fee         int64  `json:"fee"`
}

// Fee 返回交易 fee（sun）。ret 为空或解析失败=0。
func (t txJSON) Fee() int64 {
	if len(t.Ret) == 0 {
		return 0
	}
	return t.Ret[0].Fee
}

type rawDataContractJSON struct {
	Contract []contractEntryJSON `json:"contract"`
}

type contractEntryJSON struct {
	Type      string        `json:"type"`
	Parameter parameterJSON `json:"parameter"`
}

type parameterJSON struct {
	Value json.RawMessage `json:"value"`
}

// transferValueJSON TransferContract 的 parameter.value
type transferValueJSON struct {
	OwnerAddress string `json:"owner_address"`
	ToAddress    string `json:"to_address"`
	Amount       int64  `json:"amount"`
}

// SmartContractType 返回合约类型（首个 contract entry 的 type）。
func (t txJSON) SmartContractType() string {
	if len(t.RawData.Contract) == 0 {
		return ""
	}
	return t.RawData.Contract[0].Type
}

// TransferTo 返回 TransferContract 的 to_address（visible:true 时为 base58）。
func (t txJSON) TransferTo() string {
	if len(t.RawData.Contract) == 0 || t.RawData.Contract[0].Type != "TransferContract" {
		return ""
	}
	var v transferValueJSON
	if err := json.Unmarshal(t.RawData.Contract[0].Parameter.Value, &v); err != nil {
		return ""
	}
	return v.ToAddress
}

// TransferFrom 返回 TransferContract 的 owner_address（付款方）。
func (t txJSON) TransferFrom() string {
	if len(t.RawData.Contract) == 0 || t.RawData.Contract[0].Type != "TransferContract" {
		return ""
	}
	var v transferValueJSON
	if err := json.Unmarshal(t.RawData.Contract[0].Parameter.Value, &v); err != nil {
		return ""
	}
	return v.OwnerAddress
}

// TransferAmount 返回 TransferContract 的 amount（sun）。
func (t txJSON) TransferAmount() int64 {
	if len(t.RawData.Contract) == 0 || t.RawData.Contract[0].Type != "TransferContract" {
		return 0
	}
	var v transferValueJSON
	if err := json.Unmarshal(t.RawData.Contract[0].Parameter.Value, &v); err != nil {
		return 0
	}
	return v.Amount
}

// freezeValueJSON 质押类合约的 parameter.value（Freeze/StakeV2=frozen_balance；UnfreezeV2=unfreeze_balance）。
type freezeValueJSON struct {
	FrozenBalance  int64 `json:"frozen_balance"`
	UnfreezeAmount int64 `json:"unfreeze_balance"`
}

// StakeAmount 返回质押类合约的资金量（sun）：Freeze/StakeV2 取 frozen_balance，
// UnfreezeV2 取 unfreeze_balance；其他合约/解析失败=0（9-9：流水卡质押行金额显示用）。
func (t txJSON) StakeAmount() int64 {
	if len(t.RawData.Contract) == 0 {
		return 0
	}
	ct := t.RawData.Contract[0].Type
	if ct != "FreezeBalanceV2Contract" && ct != "StakeV2Contract" && ct != "UnfreezeBalanceV2Contract" {
		return 0
	}
	var v freezeValueJSON
	if err := json.Unmarshal(t.RawData.Contract[0].Parameter.Value, &v); err != nil {
		return 0
	}
	switch ct {
	case "UnfreezeBalanceV2Contract":
		return v.UnfreezeAmount
	default:
		return v.FrozenBalance
	}
}

// witnessResponseJSON 对应 listwitnesses 响应。
type witnessResponseJSON struct {
	Witnesses []witnessJSON `json:"witnesses"`
}

type witnessJSON struct {
	Address   string `json:"address"` // hex 格式（0x41 开头）
	VoteCount int64  `json:"voteCount"`
	URL       string `json:"url"`
	IsJobs    bool   `json:"isJobs"`
}

// ---------- D-231 TRX 投票收益率（TRONSCAN /api/witness + /api/vote） ----------

// tronscanWitnessJSON 对应 TRONSCAN /api/witness 单行（年化收益率采集）。
// 字段 annualizedRate=年化 %（字符串，实测 "3.111945"）；brokerage=佣金率；realTimeVotes/votes=票数；
// name=SR 名；address=SR 地址；producer=true=在任出块 SR（max_rate 只取 producer=true）。
// D-233 S1：producedTotal=出块总数（Vi 计算辅助字段，annualizedRate 已内含出块奖励故主算路径不依赖）。
type tronscanWitnessJSON struct {
	Address        string `json:"address"`
	Name           string `json:"name"`
	AnnualizedRate string `json:"annualizedRate"` // 年化 %（字符串→float）
	Brokerage      int    `json:"brokerage"`
	RealTimeVotes  int64  `json:"realTimeVotes"`
	Votes          int64  `json:"votes"`
	Producer       bool   `json:"producer"`
	ProducedTotal  int64  `json:"producedTotal"`
}

// tronscanVoteJSON 对应 TRONSCAN /api/vote?voter={addr} 单行（当前投票分布）。
// candidateAddress=候选 SR 地址；candidateName=SR 名；votes=票数；timestamp=投票时间。
// R4 修复：timestamp 实测为字符串（如 "1757054520000"，生产日志 2026-09-05 实抓），
// 非 int64——旧假设致每轮 unmarshal 败→current_rate 恒 0。字段无下游消费，保留作形状文档。
type tronscanVoteJSON struct {
	CandidateAddress string `json:"candidateAddress"`
	CandidateName    string `json:"candidateName"`
	Votes            int64  `json:"votes"`
	Timestamp        string `json:"timestamp"`
}

// tronscanVoteResponseJSON 对应 /api/vote 响应。
type tronscanVoteResponseJSON struct {
	TotalVotes int64              `json:"totalVotes"`
	Data       []tronscanVoteJSON `json:"data"`
}

// ---------- D-233 TRX 待领取实时化（getchainparameters） ----------

// chainParametersResponseJSON 对应 TronGrid /wallet/getchainparameters 响应（匿名已实测 200）。
// D-233 S1：取 blockReward/voteReward 等参数（annualizedRate 已内含故主算路径不依赖，作形状文档+交叉校验用）。
type chainParametersResponseJSON struct {
	ChainParameter []chainParameterJSON `json:"chainParameter"`
}

type chainParameterJSON struct {
	Key   string `json:"key"`
	Value int64  `json:"value"`
}

// ---------- D-279 TronScan cheatStatus ----------

// tronScanTxsResponse 对应 TronScan /api/transaction 响应。
type tronScanTxsResponse struct {
	Data []tronScanTxJSON `json:"data"`
}

type tronScanTxJSON struct {
	Hash        string `json:"hash"`
	CheatStatus bool   `json:"cheatStatus"`
}
