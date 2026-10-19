// totp.go —— RFC 6238 TOTP 验证+4h session 签发/校验（业主令 10-19：
// 静态 token 永不过期记不住→6 位码 30s 有效+session 4h；单人单 secret；
// static token 径保留=AI 席位 SSH/localhost 直调不删）。
// 零外库：TOTP=HMAC-SHA1(base32(secret), floor(t/30))±1 窗；
// session=HMAC-SHA256("exp|nonce", K=SHA256(totp_secret+"|session"))。
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateTOTPSecret 生成 base32 secret（160bit——RFC 4226 推荐长）。
func GenerateTOTPSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return b32.EncodeToString(b), nil
}

// totpAt 30s 步进 6 位码（truncated HMAC——RFC 6238 §5.3 动态截取）。
func totpAt(secretB32 string, counter int64) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secretB32)))
	if err != nil {
		return "", err
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(counter))
	m := hmac.New(sha1.New, key)
	m.Write(buf[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := (binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff) % 1000000
	return fmt.Sprintf("%06d", code), nil
}

// VerifyTOTP ±1 窗验证（30s 步进——设备微差容 1 步）。
func VerifyTOTP(secretB32, code string, now time.Time) bool {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return false
	}
	c := now.Unix() / 30
	for _, d := range []int64{-1, 0, 1} {
		if want, err := totpAt(secretB32, c+d); err == nil && hmac.Equal([]byte(want), []byte(code)) {
			return true
		}
	}
	return false
}

// IssueSession 签发 4h session=expUnix.hex|nonce.hex|sig.hex
// （K=SHA256(secret+"|session")——totp secret 衍生不重引第二密钥）。
func IssueSession(secretB32 string, now time.Time) (token string, expiresAt time.Time) {
	exp := now.Add(4 * time.Hour)
	nb := make([]byte, 12)
	_, _ = rand.Read(nb)
	body := fmt.Sprintf("%d|%s", exp.Unix(), hex.EncodeToString(nb))
	sig := sessionSig(secretB32, body)
	return body + "|" + sig, exp
}

// VerifySession HMAC 等值校验+exp 未过。secret 空=恒 false（TOTP 未配=该径死锁）。
func VerifySession(secretB32, tok string, now time.Time) bool {
	i := strings.LastIndex(tok, "|")
	if i < 0 {
		return false
	}
	body, sig := tok[:i], tok[i+1:]
	if !hmac.Equal([]byte(sig), []byte(sessionSig(secretB32, body))) {
		return false
	}
	exp, err := strconv.ParseInt(strings.SplitN(body, "|", 2)[0], 10, 64)
	return err == nil && now.Unix() < exp
}

func sessionSig(secretB32, body string) string {
	k := sha256.Sum256([]byte(strings.ToUpper(strings.TrimSpace(secretB32)) + "|session"))
	m := hmac.New(sha256.New, k[:])
	m.Write([]byte(body))
	return hex.EncodeToString(m.Sum(nil))
}

// TOTPIdCode 身份码：SHA256(归一 secret+"|idcode") 前 3 字节=6 位小写 hex。
// 业主令 10-19 追加：绑定程序太多不易区分——扫码标签与登录页同源同码，
// Authenticator 里凭 Nox:9663dc 对号。指纹面（SSH host key 同理）：单向衍生，
// 不泄 secret 本体可验证信息；secret 重生成→身份码随换（无需另存状态）。
func TOTPIdCode(secretB32 string) string {
	sum := sha256.Sum256([]byte(strings.ToUpper(strings.TrimSpace(secretB32)) + "|idcode"))
	return hex.EncodeToString(sum[:3])
}

// OTPAuthURI 设置页 QR 内容（otpauth://totp/Nox:<身份码>?secret=…&issuer=Nox）。
func OTPAuthURI(secretB32 string) string {
	return "otpauth://totp/Nox:" + TOTPIdCode(secretB32) + "?secret=" + strings.ToUpper(secretB32) + "&issuer=Nox&digits=6&period=30"
}

// TOTPLimiter 登录限速器（连续 5 败锁 5min——防暴力扫 6 位空间）。
type TOTPLimiter struct {
	mu     sync.Mutex
	fails  int
	lockTo time.Time
}

// Allow 当前可试？败+1（达 5 锁 5min）；成功→Reset。
func (l *TOTPLimiter) Allow(now time.Time) (ok bool, wait time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Before(l.lockTo) {
		return false, l.lockTo.Sub(now)
	}
	return true, 0
}

func (l *TOTPLimiter) Fail(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails++
	if l.fails >= 5 {
		l.lockTo = now.Add(5 * time.Minute)
		l.fails = 0
	}
}

func (l *TOTPLimiter) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails = 0
	l.lockTo = time.Time{}
}

var ErrTOTPRateLimited = errors.New("totp rate limited")
