package cmc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultPlatformBase multirequest/createsession 基址（生产默认值，测试可注入 httptest URL）。
const DefaultPlatformBase = "https://platform.cmcmarkets.com"

// DefaultRestBase connectapi REST 基址（生产默认值，测试可注入 httptest URL）。
const DefaultRestBase = "https://connectapi.cmcmarkets.com"

const userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// refreshFailThreshold refreshJWT 连续失败达此数 → 回退 loginUnlocked（重登兜底）。
// 阈值 3：sched.go 退避 2s/4s/8s → 第 3 次失败后约 14s 触发重登，远早于人工介入；
// 单次瞬时 5xx 不触发（避免误杀活 session）。D-214：9-3 15:47 UTC refresh 持续 500
// 断流 8h+ 实证——状态码不可信任（session 失效后 platform 返回 500 非 401），重登判断须基于连续失败计数。
const refreshFailThreshold = 3

// AuthManager 管理 CMC 认证链：createsession → SIGN_IN → 静默重登录 → JWT(432)。
// JWT 惰性刷新（< 60s）+ 主动刷新 ticker（4 分钟）+ 401 触发重新登录。
type AuthManager struct {
	email     string
	password  string
	accountID int64
	baseURL   string // platform 基址（DefaultPlatformBase；测试注入 httptest URL）

	mu           sync.RWMutex
	token        string // session token（createsession 获取）
	jwt          string // JWT（432 获取）
	jwtExp       time.Time
	site         string // createsession 响应 site header
	seq          uint64 // multirequest 序号（loginUnlocked 后终态=20，refreshJWT 递增）
	refreshFails int    // refreshJWT 连续失败计数（非 200 且非 401；成功/重登清零）—D-214 重登兜底
	httpClient   *http.Client
}

// NewAuthManager 创建 AuthManager。platformBase 生产传 DefaultPlatformBase，测试传 httptest URL。
func NewAuthManager(email, password string, accountID int64, platformBase string) *AuthManager {
	jar, _ := cookiejar.New(nil)
	return &AuthManager{
		email:     email,
		password:  password,
		accountID: accountID,
		baseURL:   platformBase,
		httpClient: &http.Client{
			Jar:     jar,
			Timeout: 30 * time.Second,
		},
	}
}

// ts 返回当前时间的 UnixMilli（毫秒级）——varint6 固定 6 字节，UnixMilli（13 位）刚好 6 字节。
// ⚠️ 必须用 UnixMilli 不能用 Unix（秒），否则 varint6 编码不足 6 字节导致模板校验失败。
func ts() uint64 { return uint64(time.Now().UnixMilli()) }

// Login 公开方法，加写锁调 loginUnlocked。
func (a *AuthManager) Login(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.loginUnlocked(ctx)
}

// loginUnlocked 内部方法，调用方持写锁。执行 Step 1-4。
func (a *AuthManager) loginUnlocked(ctx context.Context) error {
	// Step 1: createsession
	csBody, err := buildCreateSession(ts(), genUUID())
	if err != nil {
		return fmt.Errorf("buildCreateSession: %w", err)
	}
	resp, err := a.doMultirequest(ctx, "/api/createsession/ng", csBody, false)
	if err != nil {
		return fmt.Errorf("createsession: %w", err)
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return fmt.Errorf("createsession: status %d", resp.StatusCode)
	}
	a.token = resp.Header.Get("token")
	a.site = resp.Header.Get("site")
	resp.Body.Close()
	if a.token == "" {
		return errors.New("createsession: empty token header")
	}

	// Step 2: SIGN_IN_USER (seq=7)
	signInBody := buildSignIn(a.email, a.password, ts(), 7)
	if err := a.postMultirequest(ctx, signInBody, "SIGN_IN"); err != nil {
		return err
	}

	// Step 3: 静默重登录 (seq=9，解锁关键)
	silentBody := buildSilentSignIn(a.accountID, ts(), 9)
	if err := a.postMultirequest(ctx, silentBody, "SILENT_SIGNIN"); err != nil {
		return err
	}

	// Step 4: CREATE_JWT_REQUEST (seq=20)
	jwtBody := buildJwtReq(ts(), 20)
	resp, err = a.doMultirequest(ctx, "/api/multirequest", jwtBody, true)
	if err != nil {
		return fmt.Errorf("jwt request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("jwt request: status %d", resp.StatusCode)
	}
	jwtBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read jwt resp: %w", err)
	}
	jwt, ok := findJwt(jwtBytes)
	if !ok {
		return errors.New("jwt request: findJwt failed")
	}
	exp, err := parseExp(jwt)
	if err != nil {
		return fmt.Errorf("parseExp: %w", err)
	}
	a.jwt = jwt
	a.jwtExp = exp
	a.seq = 20         // 终态=20（勿留在 0——否则首次刷新发 seq=1，对服务端是序号回退）
	a.refreshFails = 0 // 登录成功=全部状态复位（D-214）
	return nil
}

// postMultirequest 发送 multirequest 并检查 200（loginUnlocked 内部辅助，调用方持写锁）。
func (a *AuthManager) postMultirequest(ctx context.Context, body []byte, name string) error {
	resp, err := a.doMultirequest(ctx, "/api/multirequest", body, true)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: status %d", name, resp.StatusCode)
	}
	return nil
}

// JWT 公开方法，加读锁检查 → 升级写锁刷新（double-check）。
func (a *AuthManager) JWT(ctx context.Context) (string, error) {
	a.mu.RLock()
	if time.Until(a.jwtExp) > 60*time.Second {
		jwt := a.jwt
		a.mu.RUnlock()
		return jwt, nil
	}
	a.mu.RUnlock()

	a.mu.Lock()
	defer a.mu.Unlock()
	if time.Until(a.jwtExp) > 60*time.Second {
		return a.jwt, nil
	}
	if err := a.refreshJWT(ctx); err != nil {
		return "", err
	}
	return a.jwt, nil
}

// refreshJWT 内部方法，调用方持写锁。401 时调 loginUnlocked。
func (a *AuthManager) refreshJWT(ctx context.Context) error {
	a.seq++
	resp, err := a.doMultirequest(ctx, "/api/multirequest", buildJwtReq(ts(), a.seq), true)
	if err != nil {
		return fmt.Errorf("refreshJWT: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 {
		return a.loginUnlocked(ctx)
	}
	if resp.StatusCode != 200 {
		a.refreshFails++
		if a.refreshFails >= refreshFailThreshold {
			log.Printf("[cmc] refreshJWT: 连续 %d 次失败（最后 status %d，非 401）→ 回退 loginUnlocked 重登兜底", a.refreshFails, resp.StatusCode)
			a.refreshFails = 0
			return a.loginUnlocked(ctx)
		}
		return fmt.Errorf("refreshJWT: status %d", resp.StatusCode)
	}
	jwtBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("refreshJWT read: %w", err)
	}
	jwt, ok := findJwt(jwtBytes)
	if !ok {
		return errors.New("refreshJWT: findJwt failed")
	}
	exp, err := parseExp(jwt)
	if err != nil {
		return fmt.Errorf("refreshJWT parseExp: %w", err)
	}
	a.jwt = jwt
	a.jwtExp = exp
	a.refreshFails = 0 // 刷新成功=计数清零（D-214）
	return nil
}

// doMultirequest 发送 POST 请求。withToken=true 时带 token header（step 2 起）。
// 不加锁（无共享状态修改）。
func (a *AuthManager) doMultirequest(ctx context.Context, path string, body []byte, withToken bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", a.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	// 请求头 = D-013 验证基线全集（9 头），逐字设置勿删减。
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Referer", a.baseURL+"/")
	req.Header.Set("Origin", a.baseURL)
	req.Header.Set("sec-fetch-dest", "empty")
	req.Header.Set("sec-fetch-mode", "cors")
	req.Header.Set("sec-fetch-site", "same-origin")
	if withToken && a.token != "" {
		req.Header.Set("token", a.token)
	}
	return a.httpClient.Do(req)
}

// StartRefresh 主动刷新 ticker goroutine（双保险，覆盖 history=24h 间隔场景）。
func (a *AuthManager) StartRefresh(ctx context.Context) {
	ticker := time.NewTicker(4 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := a.JWT(ctx); err != nil {
				log.Printf("[cmc] StartRefresh JWT: %v", err)
			}
		}
	}
}

// parseExp 解析 JWT claims 的 exp 字段（参考 D013 探针 printClaims 改写）。
// JWT claims 实测：exp/iat 是 JSON number（Go 解析为 float64），单位 Unix 秒，ttl=300s。
func parseExp(jwt string) (time.Time, error) {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return time.Time{}, errors.New("invalid JWT: expected 3 parts")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("decode claims: %w", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return time.Time{}, fmt.Errorf("unmarshal claims: %w", err)
	}
	exp, ok := m["exp"].(float64)
	if !ok {
		return time.Time{}, errors.New("no exp in claims")
	}
	return time.Unix(int64(exp), 0), nil
}

// parseFloat 是 strconv.ParseFloat 的简写（JSON 数值字段全为字符串编码，需转换）。
// D-508 S2b：脏值从「静默归零」升级为「告警留痕+归零」——CMC 面为监控快照（账户/持仓/
// 报价 facts），全量上抛需改 ~30 处调用签名，豁免列为报审项；脏值 loudly log 后仍可被
// 消费方 freshness/合理性判据剔除，优于原来无声归零。
func parseFloat(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil && s != "" {
		log.Printf("[cmc] parse float %q dirty: %v", s, err)
	}
	return v
}

// parseRFC3339Loose 宽容解析 RFC3339 时间戳（D-208 v2-A）。
// CMC orders[0].submittedTimestamp 可能含 7 位小数秒（如 ...1234567Z），
// time.Parse(time.RFC3339) 只接受最多 9 位但要求正确格式——先试标准 RFC3339，失败再试带小数秒的变体。
func parseRFC3339Loose(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	// 先试标准 RFC3339（0 或 3 位小数秒）
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	// 再试 RFC3339Nano（最多 9 位小数秒）
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("parse timestamp %q: not RFC3339/RFC3339Nano", s)
}
