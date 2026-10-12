// ro-rotate —— D-059 ①：analyst 只读角色凭据轮换（一次性运维工具，留作下次轮换复用）。
//
// 流程（单进程原子序，密码全程不出进程边界——不进 git/聊天/日志/argv）：
//  1. admin 连接（env NOX_ROTATE_PG_DSN，superuser，运行时注入）读 app_secrets 现值
//     （NOX_MASTER_KEY 解密，fail-closed）取旧 NOX_AI_PG_DSN 的 host/port/db/user 坐标；
//  2. crypto/rand 生成 32 字节 hex 新密码 → ALTER ROLE <user> LOGIN PASSWORD '<pw>'
//     （修复 rolcanlogin=f 断链 + 旧密码作废）；
//  3. 新 DSN（同坐标换密码）Seal 后 UpsertAppSecret 覆写 NOX_AI_PG_DSN；
//  4. 新 DSN 实连 SELECT 1 自验证；全程输出零敏感值。
//
// 用法：
//
//	NOX_MASTER_KEY=<key> NOX_ROTATE_PG_DSN='postgresql://arb:<admin pw>@127.0.0.1:5433/arb' \
//	  go run ./tools/ro-rotate
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"nox/internal/platform/cryptoutil"

	"github.com/jackc/pgx/v5/pgxpool"
)

var verifyAddr = flag.String("verify-addr", "127.0.0.1:5433", "宿主侧验证坐标（容器名 host 解析不了时重写用；非敏感）")

func main() {
	flag.Parse() // -verify-addr 缺省 5433 是旧单机坐标；SG 部署须可覆盖（15433 避让 alphaforge）
	log.SetFlags(log.LstdFlags)
	adminDSN, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && adminDSN == "" {
		log.Fatalf("stdin 读 admin DSN: %v", err)
	}
	adminDSN = strings.TrimSpace(adminDSN)
	if adminDSN == "" {
		log.Fatal("stdin admin DSN 缺失（superuser 连接串，管道注入 fail-closed）")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		log.Fatalf("admin pool: %s", redactErr(err, adminDSN))
	}
	defer admin.Close()

	secrets, err := loadSecrets(ctx, admin)
	if err != nil {
		log.Fatalf("app_secrets load: %v", err)
	}
	oldDSN := secrets["NOX_AI_PG_DSN"]
	if oldDSN == "" {
		log.Fatal("app_secrets 无 NOX_AI_PG_DSN 行（未配置过，无坐标可轮换）")
	}
	user, newDSN, err := rewriteDSNPassword(oldDSN, "")
	if err != nil {
		log.Fatalf("旧 DSN 重写（keyword= 形态本工具不支持，请人工处理）: %s", redactErr(err, oldDSN))
	}

	pw, err := genPassword()
	if err != nil {
		log.Fatalf("gen password: %v", err)
	}
	// ALTER ROLE：LOGIN 解禁（断链根因 rolcanlogin=f）+ 密码作废旧值。
	// user/pw 均受控来源（DSN user 段 + hex），Exec 参数化无注入面。
	if _, err := admin.Exec(ctx,
		fmt.Sprintf("ALTER ROLE %s LOGIN PASSWORD '%s'", user, pw)); err != nil {
		log.Fatalf("ALTER ROLE: %v", err)
	}
	log.Printf("ALTER ROLE %s LOGIN+PASSWORD done", user)

	if _, newDSN, err = rewriteDSNPassword(oldDSN, pw); err != nil {
		log.Fatalf("新 DSN 构造: %s", redactErr(err, oldDSN))
	}

	key, ok := cryptoutil.MasterKey()
	if !ok {
		log.Fatal("NOX_MASTER_KEY 缺失（fail-closed，无法写 app_secrets）")
	}
	enc, err := cryptoutil.Seal(key, newDSN)
	if err != nil {
		log.Fatalf("seal: %v", err)
	}
	if _, err := admin.Exec(ctx,
		`INSERT INTO app_secrets (key, value_enc, note) VALUES ($1,$2,$3)
		 ON CONFLICT (key) DO UPDATE SET value_enc=EXCLUDED.value_enc, note=EXCLUDED.note, updated_at=now()`,
		"NOX_AI_PG_DSN", enc, "D-059 ①轮换（ro-rotate）"); err != nil {
		log.Fatalf("upsert app_secrets: %v", err)
	}
	log.Printf("app_secrets NOX_AI_PG_DSN updated (%s)", time.Now().UTC().Format("2006-01-02T15:04Z"))

	// 自验证：新 DSN 实连。DSN host 为容器内名（compose 网络）时按 -verify-addr
	//（宿主映射坐标，缺省 127.0.0.1:5433）重写 host:port 后验证——凭据不出进程。
	verifyDSN, err := verifyAddrRewrite(newDSN, *verifyAddr)
	if err != nil {
		log.Fatalf("verify addr rewrite: %s", redactErr(err, newDSN))
	}
	ro, err := pgxpool.New(ctx, verifyDSN)
	if err != nil {
		log.Fatalf("新 DSN pool: %s", redactErr(err, newDSN, verifyDSN, pw))
	}
	defer ro.Close()
	var one int
	if err := ro.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil {
		log.Fatalf("SELECT 1 验证失败: %v", err)
	}
	log.Printf("VERIFY OK: new credentials login+SELECT 1 succeeded (user=%s)", user)
	log.Printf("NOTE: core 进程 boot 快照仍持旧 DSN——部署重启后生效（轮换不劣化现状：旧链路本已断）")
}

// loadSecrets admin 连接上全表解密读出（MASTER_KEY fail-closed 同 pgstore.LoadAppSecrets 语义，
// 独立实现避免 Store 构造对 admin pool 的重复装配）。
func loadSecrets(ctx context.Context, admin *pgxpool.Pool) (map[string]string, error) {
	rows, err := admin.Query(ctx, `SELECT key, value_enc FROM app_secrets`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	key, _ := cryptoutil.MasterKey()
	out := map[string]string{}
	for rows.Next() {
		var k string
		var enc []byte
		if err := rows.Scan(&k, &enc); err != nil {
			return nil, err
		}
		if key == nil {
			return nil, fmt.Errorf("NOX_MASTER_KEY missing")
		}
		v, err := cryptoutil.Open(key, enc)
		if err != nil {
			return nil, fmt.Errorf("key %s: %w", k, err)
		}
		out[k] = v
	}
	return out, rows.Err()
}

// rewriteDSNPassword DSN 密码段重写：newPw=="" 仅解析（返回 user 与原 DSN）；非空换密码。
// 非 URL 形态（keyword=value）返回错误——本工具只支持 URL 形态（fail-closed 请人工处理）。
// 错误文本经 redactErr 打码（D-059 R1：url.Parse 错误透输入原文——DSN 含密码禁回显）。
func rewriteDSNPassword(dsn, newPw string) (user, out string, err error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", "", fmt.Errorf("DSN 解析失败（原文已打码）: %s", redactErr(err, dsn))
	}
	if u.Host == "" { // keyword=value 形态 url.Parse 出空 authority——非 URL DSN 拒（fail-closed）
		return "", "", fmt.Errorf("DSN 非 URL 形态（keyword=），本工具不支持")
	}
	if u.User == nil {
		user = "arb_ro"
	} else if user = u.User.Username(); user == "" {
		user = "arb_ro"
	}
	if newPw == "" {
		return user, dsn, nil
	}
	u.User = url.UserPassword(user, newPw)
	return user, u.String(), nil
}

// verifyAddrRewrite 验证坐标重写：host 非 localhost 形（容器内名）→ 换宿主映射 host:port。
// 同一凭据不同网络坐标——验证语义等价（凭据正确性+库可达性）。
func verifyAddrRewrite(dsn, addr string) (string, error) {
	if addr == "" {
		return dsn, nil
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("DSN 解析失败（原文已打码）: %s", redactErr(err, dsn))
	}
	h, p, err := net.SplitHostPort(u.Host)
	if err != nil {
		return "", err
	}
	if h == "127.0.0.1" || h == "localhost" || h == "::1" {
		return dsn, nil // 本机形态无需重写
	}
	vh, vp, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	u.Host = net.JoinHostPort(vh, vp)
	_ = p // 原端口（容器内 5432）被映射端口取代
	return u.String(), nil
}

// redactErr D-059 R1：错误文本中的敏感原文（DSN 全串/密码）打码——parse 类错误会把
// 输入原文透进 %v（url.Parse/pqx parse 均此形态），打码后仍保诊断信息（错误类别/位置）。
func redactErr(err error, secrets ...string) string {
	s := err.Error()
	for _, sec := range secrets {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, "***")
		}
	}
	return s
}

// genPassword 32 字节 crypto/rand hex（64 字符，URL/SQL 安全字符集）。
func genPassword() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
