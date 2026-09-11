// D-211 TRX 交易分类（SSOT §4 分类规则）。
package trx

import (
	"time"

	"arb/internal/store/pgstore"
)

// classifyTx 按合约类型分类（SSOT §4）。monitorAddr 用于判断 TransferContract 方向。
// 返回 ok=false 表示不入库（P2-3 非资金移动合约）。
func classifyTx(tx txJSON, monitorAddr string, rentalPayers map[string]bool) (pgstore.TRXTx, bool) {
	ts := time.UnixMilli(tx.BlockTimestamp)
	ct := tx.SmartContractType()
	if ct == "" {
		return pgstore.TRXTx{TxID: tx.TxID, Ts: ts, Kind: "other"}, true
	}
	switch ct {
	case "TransferContract":
		from := normalizeAddr(tx.TransferFrom())
		to := normalizeAddr(tx.TransferTo())
		amount := tx.TransferAmount()
		if to == "" || from == "" {
			return pgstore.TRXTx{TxID: tx.TxID, Ts: ts, Kind: "other"}, true
		}
		// 方向判断：owner(from)=监控地址 → 转出；to=监控地址 → 转入
		if from == monitorAddr {
			cp := to
			return pgstore.TRXTx{
				TxID: tx.TxID, Ts: ts, Kind: "withdraw", AmountSun: amount, Counterparty: &cp,
			}, true
		}
		// 转入：from 是付款方；命中 rental_payers → rental_income，否则 deposit
		kind := "deposit"
		if rentalPayers[from] {
			kind = "rental_income"
		}
		cp := from
		return pgstore.TRXTx{
			TxID: tx.TxID, Ts: ts, Kind: kind, AmountSun: amount, Counterparty: &cp,
		}, true
	case "WithdrawBalanceContract":
		// D-280：amount=withdraw_amount（奖励从 allowance 转入 balance 的实际金额）
		return pgstore.TRXTx{TxID: tx.TxID, Ts: ts, Kind: "vote_claim", AmountSun: tx.WithdrawAmount}, true
	case "FreezeBalanceV2Contract", "StakeV2Contract":
		// 9-9：质押量入 amount_sun（冻结非转账但金额有意义——流水卡显示「质押 N TRX」而非「—」）
		return pgstore.TRXTx{TxID: tx.TxID, Ts: ts, Kind: "stake", AmountSun: tx.StakeAmount()}, true
	case "UnfreezeBalanceV2Contract", "WithdrawExpireUnfreezeContract":
		return pgstore.TRXTx{TxID: tx.TxID, Ts: ts, Kind: "unstake", AmountSun: tx.StakeAmount()}, true
	case "AccountPermissionUpdateContract":
		// D-279：权限变更，无转账金额，fee 是实际成本
		return pgstore.TRXTx{TxID: tx.TxID, Ts: ts, Kind: "permission_update", AmountSun: tx.Fee()}, true
	case "CancelAllUnfreezeV2Contract":
		// D-279：取消解冻，无转账金额，fee 是实际成本
		return pgstore.TRXTx{TxID: tx.TxID, Ts: ts, Kind: "unstake_cancel", AmountSun: tx.Fee()}, true
	case "VoteWitnessContract":
		// D-279 v3：投票操作入库（业主裁定），amount=fee
		return pgstore.TRXTx{TxID: tx.TxID, Ts: ts, Kind: "vote", AmountSun: tx.Fee()}, true
	case "DelegateResourceContract":
		// D-279 v3：委托资源入库（业主裁定），amount=fee
		return pgstore.TRXTx{TxID: tx.TxID, Ts: ts, Kind: "delegate", AmountSun: tx.Fee()}, true
	case "UnDelegateResourceContract":
		// D-279 v3：收回委托入库（业主裁定），amount=fee
		return pgstore.TRXTx{TxID: tx.TxID, Ts: ts, Kind: "undelegate", AmountSun: tx.Fee()}, true
	case "TransferAssetContract", "TriggerSmartContract":
		// D-279 v4：非 TRX 代币转账（TRC10/TRC20）不入库（业主裁定流水卡仅显示 TRX）
		return pgstore.TRXTx{}, false
	default:
		// D-279：未识别合约用 fee 作 amount（fee=0 → amount=0 → 显示 "—"）
		return pgstore.TRXTx{TxID: tx.TxID, Ts: ts, Kind: "other", AmountSun: tx.Fee()}, true
	}
}
