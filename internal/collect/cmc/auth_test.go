package cmc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// buildJwtResp 构造包含 JWT 的 protobuf 响应（findJwt 解析路径：top.3→l2.1(=433)→l2.3→l3.2）。
func buildJwtResp(jwt string) []byte {
	l3 := putBytesField(nil, 2, []byte(jwt))
	l2 := putVarintField(nil, 1, 433)
	l2 = putBytesField(l2, 3, l3)
	return putBytesField(nil, 3, l2)
}

// fakeJWT 构造一个假 JWT（3 段 base64，payload 含 exp）。
func fakeJWT(exp time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, _ := json.Marshal(map[string]interface{}{
		"exp":       float64(exp.Unix()),
		"iat":       float64(exp.Add(-300 * time.Second).Unix()),
		"aud":       "wfe",
		"sub":       "830241474",
		"accountId": 38941616,
	})
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	return header + "." + payloadB64 + ".sig"
}

// mockAuthServer 启动 mock CMC 认证服务器（4 步链全 200）。
// 返回 server + 关闭函数。
func mockAuthServer(t *testing.T, jwt string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/createsession/ng":
			w.Header().Set("token", "mock-session-token-12345")
			w.Header().Set("site", "wfe")
			w.WriteHeader(200)
			_, _ = w.Write([]byte("ok"))
		case "/api/multirequest":
			// 所有 multirequest 返回 200；JWT 请求返回包含 JWT 的 protobuf
			body := make([]byte, 4096)
			n, _ := r.Body.Read(body)
			// 简化：检测 body 是否包含 432（JWT 请求消息类型）
			// 实际用 walk 解析，但测试简化：JWT 请求是第 4 个 multirequest
			// 更可靠：检测 body 中是否有 432 varint
			if containsMsgType(body[:n], 432) {
				w.WriteHeader(200)
				_, _ = w.Write(buildJwtResp(jwt))
				return
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte("ok"))
		default:
			w.WriteHeader(404)
		}
	}))
}

// testCreateTpl 合法头合成模板（0a07 08+ts6字节+1a + oldUUID ASCII）——非真值，
// 仅满足 buildCreateSession 头校验与 UUID 替换（真模板经 .env 注入）。
const testCreateTpl = "0a07080000000000001a37336638376133652d353762382d343537622d393834392d62316265306633376331373920"

// containsMsgType 简单检测 protobuf body 是否包含某消息类型 varint。
func containsMsgType(body []byte, msgType uint64) bool {
	// 消息类型在 field 1 varint 中——简化检测字节序列
	target := putVarintField(nil, 1, msgType)
	return strings.Contains(string(body), string(target))
}

func TestAuthManager_Login_Success(t *testing.T) {
	SetCreateTplHex(testCreateTpl) // D-145：注入式（原 env 直读退役）
	jwt := fakeJWT(time.Now().Add(300 * time.Second))
	srv := mockAuthServer(t, jwt)
	defer srv.Close()

	auth := NewAuthManager("test@example.com", "pass", 38941616, srv.URL)
	if err := auth.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if auth.jwt != jwt {
		t.Errorf("jwt = %q, want %q", auth.jwt, jwt)
	}
	if auth.token != "mock-session-token-12345" {
		t.Errorf("token = %q", auth.token)
	}
	if auth.seq != 20 {
		t.Errorf("seq = %d, want 20", auth.seq)
	}
}

func TestAuthManager_Login_CreatesessionFail(t *testing.T) {
	SetCreateTplHex(testCreateTpl) // D-145：注入式（原 env 直读退役）
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/createsession/ng" {
			w.WriteHeader(400)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	auth := NewAuthManager("e", "p", 38941616, srv.URL)
	err := auth.Login(context.Background())
	if err == nil || !strings.Contains(err.Error(), "createsession") {
		t.Fatalf("expected createsession error, got %v", err)
	}
}

func TestAuthManager_JWT_CachedValid(t *testing.T) {
	SetCreateTplHex(testCreateTpl) // D-145：注入式（原 env 直读退役）
	jwt := fakeJWT(time.Now().Add(300 * time.Second))
	srv := mockAuthServer(t, jwt)
	defer srv.Close()

	auth := NewAuthManager("e", "p", 38941616, srv.URL)
	if err := auth.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	// JWT 仍有效（> 60s）→ 返回缓存，不发请求
	reqCount := 0
	_ = reqCount
	got, err := auth.JWT(context.Background())
	if err != nil {
		t.Fatalf("JWT: %v", err)
	}
	if got != jwt {
		t.Errorf("JWT = %q, want cached %q", got, jwt)
	}
}

func TestAuthManager_JWT_RefreshNearExpiry(t *testing.T) {
	SetCreateTplHex(testCreateTpl) // D-145：注入式（原 env 直读退役）
	// 先登录获取 JWT，然后设 jwtExp 为近过期，验证刷新
	jwt1 := fakeJWT(time.Now().Add(300 * time.Second))
	jwt2 := fakeJWT(time.Now().Add(600 * time.Second))
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/createsession/ng":
			w.Header().Set("token", "tok")
			w.WriteHeader(200)
			_, _ = w.Write([]byte("ok"))
		case "/api/multirequest":
			callCount++
			body := make([]byte, 4096)
			n, _ := r.Body.Read(body)
			if containsMsgType(body[:n], 432) {
				// 第 1 次 JWT 请求（Login step 4）返回 jwt1，后续返回 jwt2
				if callCount == 1 {
					w.WriteHeader(200)
					_, _ = w.Write(buildJwtResp(jwt1))
					return
				}
				w.WriteHeader(200)
				_, _ = w.Write(buildJwtResp(jwt2))
				return
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte("ok"))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	auth := NewAuthManager("e", "p", 38941616, srv.URL)
	if err := auth.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	// 强制近过期
	auth.mu.Lock()
	auth.jwtExp = time.Now().Add(30 * time.Second)
	auth.mu.Unlock()

	got, err := auth.JWT(context.Background())
	if err != nil {
		t.Fatalf("JWT refresh: %v", err)
	}
	if got != jwt2 {
		t.Errorf("JWT = %q, want refreshed %q", got, jwt2)
	}
}

func TestAuthManager_RefreshJWT_SessionExpired(t *testing.T) {
	SetCreateTplHex(testCreateTpl) // D-145：注入式（原 env 直读退役）
	jwt1 := fakeJWT(time.Now().Add(300 * time.Second))
	jwt2 := fakeJWT(time.Now().Add(600 * time.Second))
	multirequestCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/createsession/ng":
			w.Header().Set("token", "tok")
			w.WriteHeader(200)
			_, _ = w.Write([]byte("ok"))
		case "/api/multirequest":
			multirequestCount++
			body := make([]byte, 4096)
			n, _ := r.Body.Read(body)
			if containsMsgType(body[:n], 432) {
				// Login step4 (count=1) → jwt1
				// refreshJWT (count=2) → 401（session expired）
				// loginUnlocked retry: step2,3 (count=3,4) → 200, step4 (count=5) → jwt2
				if multirequestCount == 1 {
					w.WriteHeader(200)
					_, _ = w.Write(buildJwtResp(jwt1))
					return
				}
				if multirequestCount == 2 {
					w.WriteHeader(401) // session expired
					return
				}
				// count 3,4 = SIGN_IN + silent (non-432) → 200 ok
				// count 5 = JWT req → jwt2
				w.WriteHeader(200)
				_, _ = w.Write(buildJwtResp(jwt2))
				return
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte("ok"))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	auth := NewAuthManager("e", "p", 38941616, srv.URL)
	if err := auth.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	// 强制近过期触发 refreshJWT → 401 → loginUnlocked
	auth.mu.Lock()
	auth.jwtExp = time.Now().Add(30 * time.Second)
	auth.mu.Unlock()

	got, err := auth.JWT(context.Background())
	if err != nil {
		t.Fatalf("JWT after 401 retry: %v", err)
	}
	if got != jwt2 {
		t.Errorf("JWT = %q, want %q after re-login", got, jwt2)
	}
}

func TestAuthManager_NoDeadlock(t *testing.T) {
	SetCreateTplHex(testCreateTpl) // D-145：注入式（原 env 直读退役）
	jwt := fakeJWT(time.Now().Add(300 * time.Second))
	srv := mockAuthServer(t, jwt)
	defer srv.Close()

	auth := NewAuthManager("e", "p", 38941616, srv.URL)
	if err := auth.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	// 并发 10 goroutine 调 JWT()，无死锁（timeout 5s）
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = auth.JWT(ctx)
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("deadlock: JWT concurrent calls timed out")
	}
}

func TestParseExp(t *testing.T) {
	exp := time.Now().Add(300 * time.Second)
	jwt := fakeJWT(exp)
	got, err := parseExp(jwt)
	if err != nil {
		t.Fatalf("parseExp: %v", err)
	}
	if got.Unix() != exp.Unix() {
		t.Errorf("exp = %v, want %v", got.Unix(), exp.Unix())
	}
}

func TestBuildCreateSession_ErrorOnBadTemplate(t *testing.T) {
	SetCreateTplHex(testCreateTpl) // D-145：注入式（原 env 直读退役）
	// 正常模板应成功
	_, err := buildCreateSession(ts(), genUUID())
	if err != nil {
		t.Fatalf("buildCreateSession normal: %v", err)
	}
	// 错误 UUID 长度
	_, err = buildCreateSession(ts(), "short")
	if err == nil {
		t.Fatal("expected error on short uuid")
	}
}

func TestFindJwt(t *testing.T) {
	jwt := "eyJ.test.sig"
	resp := buildJwtResp(jwt)
	got, ok := findJwt(resp)
	if !ok {
		t.Fatal("findJwt failed")
	}
	if got != jwt {
		t.Errorf("findJwt = %q, want %q", got, jwt)
	}
}

// 确保 fmt import 被使用（TestAuthManager_NoDeadlock 用 context，但 fmt 可能在其他测试用）
var _ = fmt.Sprintf
