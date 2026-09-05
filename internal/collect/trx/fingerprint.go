// D-231 S5 账户指纹检测：votes 分布+balance+staked+allowance 算确定性指纹（排序串，顺序无关）。
// 从 collector.go 提取以保 collector.go ≤300 行（check-lines 软限）。
// D-233 R5 并入：allowance_sun 入指纹——奖励结算/待领取变化即触发对冲卡刷新（≤30s+推送时延体现）。
package trx

import (
	"sort"
	"strconv"
	"strings"
)

// computeAccountFingerprint D-231 S5：votes 分布+balance+staked+allowance 算确定性指纹（排序串，顺序无关）。
// votes 按 (vote_address, vote_count) 排序后拼接，附 balance + staked 总量 + allowance（D-233 R5）。
// 用于检测换票/质押/余额/奖励结算变化（搭车既有 30s 轮询，零新建定时器）。
func computeAccountFingerprint(votes []voteJSON, balance, staked, allowance int64) string {
	sorted := make([]voteJSON, len(votes))
	copy(sorted, votes)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].VoteAddress != sorted[j].VoteAddress {
			return sorted[i].VoteAddress < sorted[j].VoteAddress
		}
		return sorted[i].VoteCount < sorted[j].VoteCount
	})
	var sb strings.Builder
	for _, v := range sorted {
		sb.WriteString(v.VoteAddress)
		sb.WriteByte(':')
		sb.WriteString(strconv.FormatInt(v.VoteCount, 10))
		sb.WriteByte(',')
	}
	sb.WriteString("b=" + strconv.FormatInt(balance, 10) +
		",s=" + strconv.FormatInt(staked, 10) +
		",a=" + strconv.FormatInt(allowance, 10))
	return sb.String()
}
