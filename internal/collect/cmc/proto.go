// Package cmc 实现 CMC Markets Next Generation REST 采集器（D-205 Phase 1）。
// 认证链基于 D-013 验证事实：createsession → SIGN_IN → 静默重登录 → JWT(432)。
// protobuf 手写 protowire 编解码（无外部依赖），函数从 D013 探针复制 + 命名映射。
package cmc

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// ---------- 极简 protobuf 编码（从独立复核探针复制，命名统一） ----------

func putVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func putTag(b []byte, field, wire int) []byte { return putVarint(b, uint64(field)<<3|uint64(wire)) }

func putBytesField(b []byte, field int, payload []byte) []byte {
	b = putTag(b, field, 2)
	b = putVarint(b, uint64(len(payload)))
	return append(b, payload...)
}

func putStringField(b []byte, field int, s string) []byte { return putBytesField(b, field, []byte(s)) }

func putVarintField(b []byte, field int, v uint64) []byte {
	b = putTag(b, field, 0)
	return putVarint(b, v)
}

// varint6 定长 6 字节 varint（当前毫秒时间戳恰好需要 6 字节，与模板长度一致）。
// ⚠️ 必须用 UnixMilli（13 位）——用 Unix（秒）会不足 6 字节导致模板校验失败。
func varint6(v uint64) []byte {
	out := make([]byte, 6)
	for i := 0; i < 6; i++ {
		out[i] = byte(v & 0x7f)
		if i < 5 {
			out[i] |= 0x80
		}
		v >>= 7
	}
	return out
}

// ---------- 极简 protobuf 解码 ----------

type fld struct {
	n    int
	wire int
	v    uint64
	data []byte
}

func walk(b []byte) ([]fld, error) {
	var out []fld
	for len(b) > 0 {
		tag, n := binary.Uvarint(b)
		if n <= 0 {
			return nil, fmt.Errorf("bad tag at %d", len(b))
		}
		b = b[n:]
		f := fld{n: int(tag >> 3), wire: int(tag & 7)}
		switch f.wire {
		case 0:
			v, n := binary.Uvarint(b)
			if n <= 0 {
				return nil, fmt.Errorf("bad varint")
			}
			f.v = v
			b = b[n:]
		case 2:
			l, n := binary.Uvarint(b)
			if n <= 0 || uint64(len(b)-n) < l {
				return nil, fmt.Errorf("bad len")
			}
			f.data = b[n : n+int(l)]
			b = b[n+int(l):]
		case 5:
			if len(b) < 4 {
				return nil, fmt.Errorf("short fixed32")
			}
			f.data = b[:4]
			b = b[4:]
		case 1:
			if len(b) < 8 {
				return nil, fmt.Errorf("short fixed64")
			}
			f.data = b[:8]
			b = b[8:]
		default:
			return nil, fmt.Errorf("wire %d", f.wire)
		}
		out = append(out, f)
	}
	return out, nil
}

func first(fields []fld, n int) *fld {
	for i := range fields {
		if fields[i].n == n {
			return &fields[i]
		}
	}
	return nil
}

// ---------- createsession 模板（D-011 验证，UUID/timestamp 运行时替换） ----------

// createTplHex env 注入（2026-09-16 CMC 断流修复·方案 B）：
// 原值系 D-011 抓包的 protobuf 协议帧模板（594 hex，运行时替换 UUID/timestamp，
// 内嵌 UA/Asia-Tehran 时区——非活凭据），9-14 E3 凭据清洗被 64+hex 规则误伤为占位符，
// 兜底重登路径 100% 失效致断流 19.4h（调查=docs/调查-CMC采集失效根因-2026-09-16-Devin.md）。
// 移出仓库：.env ARB_CMC_CREATE_TPL 注入（compose 透传），免疫未来凭据清洗。
// 惰性每次读（包级 var 在 import 期求值，t.Setenv 测试注入会错过）；登录低频开销可忽略。
// 空/非法时 fail-closed：buildCreateSession 返回 error 上抛，采集器停转非崩溃。
func createTplHex() string { return os.Getenv("ARB_CMC_CREATE_TPL") }

const oldUUID = "73f87a3e-57b8-457b-9849-b1be0f37c179"

// genUUID 用 crypto/rand 生成 UUID v4（两探针均有，buildCreateSession 的 uuid 参数）。
func genUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// buildCreateSession 模板重放：替换 timestamp varint + UUID。
// v7：探针的模板头校验 panic 改为返回 error（panic 会崩整个进程，D-188 同族）。
func buildCreateSession(ts uint64, uuid string) ([]byte, error) {
	tpl, err := hex.DecodeString(createTplHex())
	if err != nil {
		return nil, fmt.Errorf("createTpl hex decode: %w", err)
	}
	if len(tpl) < 10 || tpl[0] != 0x0a || tpl[1] != 0x07 || tpl[2] != 0x08 || tpl[9] != 0x1a {
		return nil, fmt.Errorf("createTpl header mismatch")
	}
	copy(tpl[3:9], varint6(ts))
	if len(uuid) != len(oldUUID) {
		return nil, fmt.Errorf("uuid length mismatch: got %d want %d", len(uuid), len(oldUUID))
	}
	tpl = bytes.Replace(tpl, []byte(oldUUID), []byte(uuid), 1)
	return tpl, nil
}

// wrap 外层封装：field 1=ts varint, field 3=msg3 bytes。
func wrap(msg3 []byte, ts uint64) []byte {
	f1 := putVarintField(nil, 1, ts)
	body := putBytesField(nil, 1, f1)
	return putBytesField(body, 3, msg3)
}

// buildSignIn SIGN_IN_USER 消息（seq=7）。
func buildSignIn(email, pass string, ts, seq uint64) []byte {
	inner := putStringField(nil, 1, email)
	inner = putStringField(inner, 2, pass)
	inner = putVarintField(inner, 3, 3)  // Live
	inner = putVarintField(inner, 12, 0) // 与实测模板一致
	msg3 := putVarintField(nil, 1, 12)   // SIGN_IN_USER
	msg3 = putVarintField(msg3, 2, 1)
	msg3 = putBytesField(msg3, 3, inner)
	msg3 = putVarintField(msg3, 4, seq)
	return wrap(msg3, ts)
}

// buildSilentSignIn 静默重登录消息（seq=9，解锁关键）。
// 从 D013 探针 silentSignIn 复制 + 函数名映射（vf→putVarintField, bf→putBytesField）+ accountID 参数化。
func buildSilentSignIn(accountID int64, ts, seq uint64) []byte {
	in := putVarintField(nil, 3, 2)
	in = putVarintField(in, 6, uint64(accountID)) // 参数化，不硬编码
	in = putVarintField(in, 12, 1)
	return wrap(putVarintField(putBytesField(putVarintField(putVarintField(nil, 1, 12), 2, 1), 3, in), 4, seq), ts)
}

// buildJwtReq CREATE_JWT_REQUEST 消息（seq=20）。
func buildJwtReq(ts, seq uint64) []byte {
	inner := putVarintField(nil, 2, 2) // 2=PLATFORM_API_JWT（实测 aud=wfe）
	msg3 := putVarintField(nil, 1, 432)
	msg3 = putVarintField(msg3, 2, 1)
	msg3 = putBytesField(msg3, 3, inner)
	msg3 = putVarintField(msg3, 4, seq)
	return wrap(msg3, ts)
}

// findJwt 从 433 响应解析 JWT。
// 从 D013 探针复制 + 字段名映射（m.d→m.data）。
// 嵌套路径：top.3 → l2.1(=433 消息类型) → l2.3 → l3.2(="eyJ..." JWT 字符串)。
func findJwt(b []byte) (string, bool) {
	top, err := walk(b)
	if err != nil {
		return "", false
	}
	m := first(top, 3)
	if m == nil {
		return "", false
	}
	l2, _ := walk(m.data)
	if mt := first(l2, 1); mt == nil || mt.v != 433 {
		return "", false
	}
	p := first(l2, 3)
	if p == nil {
		return "", false
	}
	l3, _ := walk(p.data)
	if jw := first(l3, 2); jw != nil && strings.HasPrefix(string(jw.data), "eyJ") {
		return string(jw.data), true
	}
	return "", false
}
