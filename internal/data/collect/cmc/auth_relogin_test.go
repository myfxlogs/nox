package cmc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// jwtReqCounter 在 mock handler 内对 432（JWT）multirequest 单独计数，
// 排除 SIGN_IN/silent 等 non-432 multirequest 的干扰，使分支与提示词 count 语义对齐。
type jwtReqCounter struct {
	n int32
}

func (c *jwtReqCounter) next() int32 { return atomic.AddInt32(&c.n, 1) }

// TestAuthManager_RefreshJWT_ConsecutiveFailsRelogin：refresh 连续 3 次 500（非 401）→ 回退
// loginUnlocked 重登兜底，最终拿到 jwt2。D-214 核心场景：服务端 session 失效返回 500 非 401，
// refreshJWT 仅 401 回退会陷入死循环；连续失败计数 ≥3 触发重登自愈。
func TestAuthManager_RefreshJWT_ConsecutiveFailsRelogin(t *testing.T) {
	SetCreateTplHex(testCreateTpl) // D-145：注入式（原 env 直读退役）
	jwt1 := fakeJWT(time.Now().Add(300 * time.Second))
	jwt2 := fakeJWT(time.Now().Add(600 * time.Second))
	var jc jwtReqCounter
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/createsession/ng":
			w.Header().Set("token", "tok")
			w.WriteHeader(200)
			_, _ = w.Write([]byte("ok"))
		case "/api/multirequest":
			body := make([]byte, 4096)
			n, _ := r.Body.Read(body)
			if !containsMsgType(body[:n], 432) {
				// SIGN_IN/silent 等 non-432 → 200 ok
				w.WriteHeader(200)
				_, _ = w.Write([]byte("ok"))
				return
			}
			// 432（JWT）请求分支：
			//   c=1 Login step4 → jwt1
			//   c=2,3,4 refreshJWT 连续 3 次 500 → refreshFails 达 3 → 回退 loginUnlocked
			//   c=5 loginUnlocked step4 JWT req → jwt2
			switch c := jc.next(); c {
			case 1:
				w.WriteHeader(200)
				_, _ = w.Write(buildJwtResp(jwt1))
			case 2, 3, 4:
				w.WriteHeader(500) // refresh 连续失败（非 401）
			default:
				w.WriteHeader(200)
				_, _ = w.Write(buildJwtResp(jwt2)) // 重登链 JWT req
			}
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	auth := NewAuthManager("e", "p", 38941616, srv.URL)
	if err := auth.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	// 强制近过期 → 每次 JWT() 走 refreshJWT。
	// JWT() 单次只调一次 refreshJWT；连续失败计数靠多次调用（scheduler 层重试语义）累积：
	//   call 1: c=2 → 500, fails=1, 返回 error
	//   call 2: c=3 → 500, fails=2, 返回 error
	//   call 3: c=4 → 500, fails=3 → 回退 loginUnlocked（c=5 → jwt2）
	auth.mu.Lock()
	auth.jwtExp = time.Now().Add(30 * time.Second)
	auth.mu.Unlock()

	for i := 1; i <= 2; i++ {
		if _, err := auth.JWT(context.Background()); err == nil {
			t.Fatalf("call %d: expected refresh 500 error, got nil", i)
		}
	}
	got, err := auth.JWT(context.Background()) // 第 3 次 → fails 达阈值 → 重登
	if err != nil {
		t.Fatalf("JWT after consecutive-fail relogin: %v", err)
	}
	if got != jwt2 {
		t.Errorf("JWT = %q, want %q (relogin jwt2)", got, jwt2)
	}
}

// TestAuthManager_RefreshJWT_TransientFailNoRelogin：refresh 单次瞬时 500 后立即 200 成功 →
// 不触发重登（createsession 仅 Login 时调用 1 次）。防误杀活 session。
func TestAuthManager_RefreshJWT_TransientFailNoRelogin(t *testing.T) {
	SetCreateTplHex(testCreateTpl) // D-145：注入式（原 env 直读退役）
	jwt1 := fakeJWT(time.Now().Add(300 * time.Second))
	jwt2 := fakeJWT(time.Now().Add(600 * time.Second))
	var jc jwtReqCounter
	var createSessionCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/createsession/ng":
			atomic.AddInt32(&createSessionCount, 1)
			w.Header().Set("token", "tok")
			w.WriteHeader(200)
			_, _ = w.Write([]byte("ok"))
		case "/api/multirequest":
			body := make([]byte, 4096)
			n, _ := r.Body.Read(body)
			if !containsMsgType(body[:n], 432) {
				w.WriteHeader(200)
				_, _ = w.Write([]byte("ok"))
				return
			}
			// c=1 Login step4 → jwt1
			// c=2 refresh 第 1 次 → 500（refreshFails=1，未达阈值，不重登）
			// c=3 refresh 第 2 次 → 200 jwt2（refreshFails 清零）
			switch c := jc.next(); c {
			case 1:
				w.WriteHeader(200)
				_, _ = w.Write(buildJwtResp(jwt1))
			case 2:
				w.WriteHeader(500) // 瞬时失败
			default:
				w.WriteHeader(200)
				_, _ = w.Write(buildJwtResp(jwt2))
			}
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	auth := NewAuthManager("e", "p", 38941616, srv.URL)
	if err := auth.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if got := atomic.LoadInt32(&createSessionCount); got != 1 {
		t.Fatalf("post-Login createSessionCount=%d, want 1", got)
	}
	auth.mu.Lock()
	auth.jwtExp = time.Now().Add(30 * time.Second)
	auth.mu.Unlock()

	// call 1: c=2 → 500（fails=1，未达阈值）→ error
	if _, err := auth.JWT(context.Background()); err == nil {
		t.Fatal("call 1: expected transient 500 error, got nil")
	}
	// call 2: c=3 → 200 jwt2（fails 清零）
	got, err := auth.JWT(context.Background())
	if err != nil {
		t.Fatalf("JWT: %v", err)
	}
	if got != jwt2 {
		t.Errorf("JWT = %q, want %q", got, jwt2)
	}
	// 单次瞬时失败不应触发重登 → createsession 仍只被 Login 调过 1 次
	if cs := atomic.LoadInt32(&createSessionCount); cs != 1 {
		t.Errorf("createSessionCount=%d, want 1 (transient fail must not relogin)", cs)
	}
}

// TestAuthManager_RefreshJWT_SuccessResetsCounter：跨两轮 refresh 序列，成功清零使计数不跨轮累计。
// 若清零遗漏，第二轮第 2 次 500 会使计数累计到 3 触发重登（createsession 升至 2）→ 防「清零遗漏」回归。
//
// 432 请求序列（jc 计数）：
//
//	c=1 Login step4 → jwt1
//	c=2 seq-A refresh → 500（fails=1）   c=3 seq-A refresh → 200 jwt2（fails 清零）
//	[强制近过期]
//	c=4 seq-B refresh → 500（fails=1）   c=5 seq-B refresh → 500（fails=2）   c=6 seq-B refresh → 200 jwt3（fails 清零）
//
// 清零正确：全程 fails 峰值=2，不重登，createsession==1。
// 清零遗漏：c=3 不清零（fails=1）→ c=4 fails=2 → c=5 fails=3 → 回退 loginUnlocked（createsession=2）→ 回归锚命中。
func TestAuthManager_RefreshJWT_SuccessResetsCounter(t *testing.T) {
	SetCreateTplHex(testCreateTpl) // D-145：注入式（原 env 直读退役）
	jwt1 := fakeJWT(time.Now().Add(300 * time.Second))
	jwt2 := fakeJWT(time.Now().Add(600 * time.Second))
	jwt3 := fakeJWT(time.Now().Add(900 * time.Second))
	var jc jwtReqCounter
	var createSessionCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/createsession/ng":
			atomic.AddInt32(&createSessionCount, 1)
			w.Header().Set("token", "tok")
			w.WriteHeader(200)
			_, _ = w.Write([]byte("ok"))
		case "/api/multirequest":
			body := make([]byte, 4096)
			n, _ := r.Body.Read(body)
			if !containsMsgType(body[:n], 432) {
				w.WriteHeader(200)
				_, _ = w.Write([]byte("ok"))
				return
			}
			switch c := jc.next(); c {
			case 1:
				w.WriteHeader(200)
				_, _ = w.Write(buildJwtResp(jwt1))
			case 2, 4, 5:
				w.WriteHeader(500) // seq-A 第 1 次 / seq-B 第 1、2 次
			case 3:
				w.WriteHeader(200)
				_, _ = w.Write(buildJwtResp(jwt2)) // seq-A 成功
			default:
				w.WriteHeader(200)
				_, _ = w.Write(buildJwtResp(jwt3)) // seq-B 成功 / 重登链兜底
			}
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	auth := NewAuthManager("e", "p", 38941616, srv.URL)
	if err := auth.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}

	// 序列 A：一次 500 一次 200（成功清零）
	//   call 1: c=2 → 500（fails=1）→ error
	//   call 2: c=3 → 200 jwt2（fails 清零）
	auth.mu.Lock()
	auth.jwtExp = time.Now().Add(30 * time.Second)
	auth.mu.Unlock()
	if _, err := auth.JWT(context.Background()); err == nil {
		t.Fatal("seq-A call 1: expected 500 error, got nil")
	}
	got, err := auth.JWT(context.Background())
	if err != nil {
		t.Fatalf("JWT seq-A: %v", err)
	}
	if got != jwt2 {
		t.Errorf("seq-A JWT = %q, want %q", got, jwt2)
	}

	// 序列 B：两次 500 一次 200（清零正确则 fails 峰值=2 不重登；遗漏则 c=5 累计到 3 触发重登）
	//   call 3: c=4 → 500（fails=1）→ error
	//   call 4: c=5 → 500（fails=2）→ error
	//   call 5: c=6 → 200 jwt3（fails 清零）
	auth.mu.Lock()
	auth.jwtExp = time.Now().Add(30 * time.Second)
	auth.mu.Unlock()
	for i := 3; i <= 4; i++ {
		if _, err := auth.JWT(context.Background()); err == nil {
			t.Fatalf("seq-B call %d: expected 500 error, got nil", i)
		}
	}
	got, err = auth.JWT(context.Background())
	if err != nil {
		t.Fatalf("JWT seq-B: %v", err)
	}
	if got != jwt3 {
		t.Errorf("seq-B JWT = %q, want %q", got, jwt3)
	}
	// 全程不应重登 → createsession 仍 ==1（清零遗漏会升至 2）
	if cs := atomic.LoadInt32(&createSessionCount); cs != 1 {
		t.Errorf("createSessionCount=%d, want 1 (success must reset refreshFails; got relogin)", cs)
	}
}
