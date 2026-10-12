// D-278 S5：TRX 地址资源状态计算（剩余带宽/能量 → normal/yellow/red）。
// 阈值与 trxvote 预检常量同源引用（禁双写）：yellow=改投线 BandwidthNeedVoteBP，red=领奖线 BandwidthNeedClaimBP。
package dashboard

import (
	"nox/internal/portfolio/trxvote"
	"nox/internal/store/pgstore"
)

// bandwidthStatus 计算剩余带宽状态：剩余=(FreeNetLimit-FreeNetUsed)+(NetLimit-NetUsed)。
// normal=充足 / yellow=<改投线（500 BP，改投将跳过）/ red=<领奖线（250 BP，领奖将跳过）。
// 阈值引用 trxvote.BandwidthNeedVoteBP/BandwidthNeedClaimBP（同源禁双写）。
func bandwidthStatus(a pgstore.TRXAccount) string {
	free := a.FreeNetLimit - a.FreeNetUsed
	staked := a.NetLimit - a.NetUsed
	remaining := free + staked
	if remaining < trxvote.BandwidthNeedClaimBP {
		return "red"
	}
	if remaining < trxvote.BandwidthNeedVoteBP {
		return "yellow"
	}
	return "normal"
}
