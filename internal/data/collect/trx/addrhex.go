// Package trx —— TRON 地址 hex↔base58 转换（止血5：REST 端点 visible 无效，
// parameter.value 内地址恒为 41 前缀 hex；D-210 经验库 base58check-decode-fixed-length 正主）。
package trx

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

const b58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// hexToBase58 将 41 前缀 hex 地址转为 base58check（T 前缀）。
func hexToBase58(h string) (string, error) {
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != 21 || b[0] != 0x41 {
		return "", errors.New("bad tron hex address")
	}
	sum := sha256.Sum256(b)
	sum = sha256.Sum256(sum[:])
	b = append(b, sum[:4]...) // base58check 校验 4 字节
	var out []byte
	for i := 0; i < len(b); i++ { // big-divide 编码
		carry := int(b[i])
		for j := 0; j < len(out); j++ {
			carry += int(out[j]) << 8
			out[j] = byte(carry % 58)
			carry /= 58
		}
		for carry > 0 {
			out = append(out, byte(carry%58))
			carry /= 58
		}
	}
	for _, c := range b { // 前导 0 → '1'
		if c != 0 {
			break
		}
		out = append(out, 0)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	s := make([]byte, len(out))
	for i, v := range out {
		s[i] = b58Alphabet[v]
	}
	return string(s), nil
}

// normalizeAddr 入参若是 41 前缀 hex 转 base58，已是 base58（T 前缀）原样返回。
func normalizeAddr(a string) string {
	if len(a) == 42 && a[:2] == "41" {
		if b58, err := hexToBase58(a); err == nil {
			return b58
		}
	}
	return a
}
