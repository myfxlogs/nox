// neutralize.go —— LLM 输入中性化（D-087）：审查对抗词表映射，中文金融触发词→英文术语。
// 咽喉位点唯一：StreamMessages 发送前对载荷就地映射（analyst loop / explorer / judge 全走该路径）。
// 边界：只动发送前载荷文本——不动 SSE 解析、不动调用方、不动工具协议字段。

package analyst

import (
	"bytes"
	"encoding/json"
	"strings"
)

// neutralizePairs 审查对抗词表（D-087，域值禁令豁免——业主裁定：映射表代码内联常量，
// 补表=常量更新不走票）。长度降序构造：strings.NewReplacer 同位置按参数序先匹配，
// 长者先赢（防子串截胡——持仓盈亏 必须先于 持仓/盈亏，强制平仓 先于 平仓/强平）。
var neutralizePairs = []string{
	// 4 字
	"持仓盈亏", "position PnL",
	"强制平仓", "forced liquidation",
	// 3 字
	"保证金", "margin",
	"收益率", "return rate",
	// 2 字
	"持仓", "position",
	"平仓", "close position",
	"开仓", "open position",
	"建仓", "build position",
	"加仓", "add to position",
	"减仓", "reduce position",
	"清仓", "close all positions",
	"重仓", "heavy position",
	"轻仓", "light position",
	"满仓", "full position",
	"底仓", "base position",
	"做多", "go long",
	"做空", "go short",
	"开多", "open long",
	"开空", "open short",
	"平多", "close long",
	"平空", "close short",
	"爆仓", "liquidation",
	"强平", "forced liquidation",
	"杠杆", "leverage",
	"止损", "stop-loss",
	"止盈", "take-profit",
	"浮盈", "unrealized profit",
	"浮亏", "unrealized loss",
	"盈亏", "PnL",
	"年化", "annualized",
	"盈利", "profit",
	"亏损", "loss",
	"买入", "buy",
	"卖出", "sell",
	"追涨", "chase the rally",
	"抄底", "buy the dip",
	"荐股", "stock-picking advice",
}

var neutralizeReplacer = strings.NewReplacer(neutralizePairs...)

// NeutralizeText 单遍替换中性化（strings.NewReplacer 天然无级联：替换产物不重扫）。
// 幂等：英文/数字/符号零触碰，不含触发词载荷零变换。
func NeutralizeText(s string) string {
	return neutralizeReplacer.Replace(s)
}

// NeutralizeRequest 就地中性化发送载荷的模型可见文本（D-087）：
// System + Messages[].Content（string 与 []ContentBlock 两形态）+ tool_result 的
// Content（再一层 string/[]block 两形态，递归一层即止——协议嵌套深度封顶）。
// Input/ID/Name 不动——模型产物原样回传，改写破坏工具协议。
// 零变换时 Content 原字节保留（不重序列化）；两形态解析皆败（坏 JSON）原样放行。
func NeutralizeRequest(req *MessageRequest) {
	if req == nil {
		return
	}
	req.System = NeutralizeText(req.System)
	for i := range req.Messages {
		m := &req.Messages[i]
		var text string
		if err := json.Unmarshal(m.Content, &text); err == nil {
			if nt := NeutralizeText(text); nt != text {
				if nb, err := json.Marshal(nt); err == nil {
					m.Content = nb
				}
			}
			continue
		}
		var blocks []ContentBlock
		if err := json.Unmarshal(m.Content, &blocks); err == nil {
			neutralizeBlocks(blocks, true)
			if nb, err := json.Marshal(blocks); err == nil && !bytes.Equal(nb, m.Content) {
				m.Content = nb
			}
		}
	}
}

// neutralizeBlocks 逐块映射 Text；walkContent=true 时再下钻 tool_result 的 Content
// （string/[]block 两形态；[]block 只映 Text 不再下钻——递归一层即止）。
func neutralizeBlocks(blocks []ContentBlock, walkContent bool) {
	for i := range blocks {
		b := &blocks[i]
		b.Text = NeutralizeText(b.Text)
		if !walkContent || len(b.Content) == 0 {
			continue
		}
		var s string
		if err := json.Unmarshal(b.Content, &s); err == nil {
			if ns := NeutralizeText(s); ns != s {
				if nb, err := json.Marshal(ns); err == nil {
					b.Content = nb
				}
			}
			continue
		}
		var inner []ContentBlock
		if err := json.Unmarshal(b.Content, &inner); err == nil {
			neutralizeBlocks(inner, false)
			if nb, err := json.Marshal(inner); err == nil && !bytes.Equal(nb, b.Content) {
				b.Content = nb
			}
		}
	}
}
