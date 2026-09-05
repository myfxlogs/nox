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
type resourceJSON struct {
	NetLimit       int64 `json:"NetLimit"`
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
type tronscanWitnessJSON struct {
	Address        string `json:"address"`
	Name           string `json:"name"`
	AnnualizedRate string `json:"annualizedRate"` // 年化 %（字符串→float）
	Brokerage      int    `json:"brokerage"`
	RealTimeVotes  int64  `json:"realTimeVotes"`
	Votes          int64  `json:"votes"`
	Producer       bool   `json:"producer"`
}

// tronscanWitnessResponseJSON 对应 /api/witness 响应（数组在 data 字段）。
type tronscanWitnessResponseJSON struct {
	Data []tronscanWitnessJSON `json:"data"`
}

// tronscanVoteJSON 对应 TRONSCAN /api/vote?voter={addr} 单行（当前投票分布）。
// candidateAddress=候选 SR 地址；candidateName=SR 名；votes=票数；timestamp=投票时间。
type tronscanVoteJSON struct {
	CandidateAddress string `json:"candidateAddress"`
	CandidateName    string `json:"candidateName"`
	Votes            int64  `json:"votes"`
	Timestamp        int64  `json:"timestamp"`
}

// tronscanVoteResponseJSON 对应 /api/vote 响应。
type tronscanVoteResponseJSON struct {
	TotalVotes int64              `json:"totalVotes"`
	Data       []tronscanVoteJSON `json:"data"`
}
