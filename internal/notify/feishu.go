// D-264 飞书告警推送客户端：tenant_access_token 缓存 + im/v1/messages 发单聊文本。
// 国内直连（open.feishu.cn）；失败只返回错误，调用方 log 不阻断告警落库。
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// feishuBase var 而非 const：测试可指向 httptest server。
var feishuBase = "https://open.feishu.cn/open-apis"

// FeishuClient 自定义应用机器人客户端（app_id/app_secret 模式）。
type FeishuClient struct {
	appID     string
	appSecret string
	hc        *http.Client

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

// NewFeishuClient 空 appID=禁用（IsZero 语义，装配层判空跳过）。
func NewFeishuClient(appID, appSecret string) *FeishuClient {
	return &FeishuClient{appID: appID, appSecret: appSecret, hc: &http.Client{Timeout: 10 * time.Second}}
}

// Enabled 配置齐才启用。
func (c *FeishuClient) Enabled() bool { return c != nil && c.appID != "" && c.appSecret != "" }

type feishuTokenResp struct {
	Code              int    `json:"code"`
	Msg               string `json:"msg"`
	TenantAccessToken string `json:"tenant_access_token"`
	Expire            int    `json:"expire"` // 秒
}

func (c *FeishuClient) getToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.tokenExp) {
		return c.token, nil
	}
	body, _ := json.Marshal(map[string]string{"app_id": c.appID, "app_secret": c.appSecret})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, feishuBase+"/auth/v3/tenant_access_token/internal", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("feishu token: %w", err)
	}
	defer resp.Body.Close()
	var tr feishuTokenResp
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", fmt.Errorf("feishu token decode: %w", err)
	}
	if tr.Code != 0 || tr.TenantAccessToken == "" {
		return "", fmt.Errorf("feishu token: code=%d msg=%s", tr.Code, tr.Msg)
	}
	// 提前 5 分钟过期防临界
	c.token = tr.TenantAccessToken
	c.tokenExp = time.Now().Add(time.Duration(tr.Expire)*time.Second - 5*time.Minute)
	return c.token, nil
}

type feishuSendResp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		MessageID string `json:"message_id"` // D-168：episode push_msg_id 对位（引用回复 ack）
	} `json:"data"`
}

// SendText 向 chat_id 发文本消息（msg_type=text），返回 message_id。
func (c *FeishuClient) SendText(ctx context.Context, chatID, text string) (string, error) {
	if !c.Enabled() {
		return "", fmt.Errorf("feishu client disabled")
	}
	if chatID == "" {
		return "", fmt.Errorf("feishu send: empty chat_id（未绑定，先给机器人发条消息）")
	}
	tok, err := c.getToken(ctx)
	if err != nil {
		return "", err
	}
	content, _ := json.Marshal(map[string]string{"text": text})
	body, _ := json.Marshal(map[string]string{
		"receive_id": chatID,
		"msg_type":   "text",
		"content":    string(content),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, feishuBase+"/im/v1/messages?receive_id_type=chat_id", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("feishu send: %w", err)
	}
	defer resp.Body.Close()
	var sr feishuSendResp
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return "", fmt.Errorf("feishu send decode: %w", err)
	}
	if sr.Code != 0 {
		return "", fmt.Errorf("feishu send: code=%d msg=%s", sr.Code, sr.Msg)
	}
	return sr.Data.MessageID, nil
}
