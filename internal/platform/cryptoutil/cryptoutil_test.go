// cryptoutil_test.go —— D-515 A2：Seal/Open 往返+错 key 拒解+nonce 随机性。
package cryptoutil

import (
	"bytes"
	"testing"
)

func TestD515_SealOpen(t *testing.T) {
	key, ok := MasterKey()
	if !ok {
		// 测试不依赖 env——用固定测试 key。
		key = []byte("testkey")
	}
	ct, err := Seal(key, "secret-value-123")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	pt, err := Open(key, ct)
	if err != nil || pt != "secret-value-123" {
		t.Fatalf("roundtrip: %q %v", pt, err)
	}
	// 错 key 拒解。
	if _, err := Open([]byte("wrongkey"), ct); err == nil {
		t.Fatal("wrong key should fail")
	}
	// nonce 随机性：同明文两密文不同。
	ct2, _ := Seal(key, "secret-value-123")
	if bytes.Equal(ct, ct2) {
		t.Fatal("nonce not random")
	}
	// 篡改拒解。
	ct3 := append([]byte(nil), ct...)
	ct3[len(ct3)-1] ^= 0xff
	if _, err := Open(key, ct3); err == nil {
		t.Fatal("tampered blob should fail")
	}
	// 空 key 拒。
	if _, err := Seal(nil, "x"); err == nil {
		t.Fatal("empty key should fail")
	}
}
