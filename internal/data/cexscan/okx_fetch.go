// okx_fetch.go —— D-011 S2：OKX 直连→cex-proxy 回落骨架（spread/earn 两采集面共用）。
// 同源脆弱面修复：spread/earn 硬编码直连 www.okx.com，mihomo 断线即全灭（okx spot 配置行
// base_url 已指向 cex-proxy，生产实拍）。回落语义同构 httpx failoverRT（D-301b）：仅传输层
// 错误（RST/超时/DNS——链路故障形态）触发；HTTP 已到达（4xx/5xx/业务码≠0 如 51001 品种
// 映射语义）不回落。每轮先试直连，直连恢复自动回主（无粘滞）。
package cexscan

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

// okxDirectBase 直连主路径基址（生产现行硬编码值；var 仅为测试可覆写端点，depth_sampler 先例）。
var okxDirectBase = "https://www.okx.com"

// okxGet 单基址 GET：返回 (body, arrived, err)。
// arrived=true=HTTP 已到达（任何状态码——链路通，后续错误属 venue 语义）；
// false=传输层失败（回落唯一触发条件）。hdr 回调每次尝试现调（earn 签名 ts 每试新鲜）。
func okxGet(ctx context.Context, client *http.Client, base, path string, hdr func(*http.Request)) ([]byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+path, nil)
	if err != nil {
		return nil, false, err
	}
	if hdr != nil {
		hdr(req)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return body, true, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, true, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	return body, true, nil
}

// okxFetch 直连优先，传输层错误回落 okxBase（exchange_configs okx spot base_url=cex-proxy）。
// okxBase 空（无 okx spot 配置行）=无回落通道，保持仅直连不造回落。
// 回落也失败=原直连错上抛（附代理错，不掩盖主因）；回落成功日志 src=proxy 溯源可辨。
func okxFetch(ctx context.Context, client *http.Client, okxBase, path string, hdr func(*http.Request)) ([]byte, error) {
	body, arrived, err := okxGet(ctx, client, okxDirectBase, path, hdr)
	if arrived {
		return body, err
	}
	if okxBase == "" {
		return nil, err
	}
	proxyBody, _, perr := okxGet(ctx, client, okxBase, path, hdr)
	if perr != nil {
		return nil, fmt.Errorf("direct: %w; proxy: %v", err, perr)
	}
	log.Printf("[cexscan] okx %s 直连失败回落 cex-proxy（src=proxy）: %v", path, err)
	return proxyBody, nil
}
