// Package cryptoutil D-515 §2：AES-256-GCM 密钥封解（nonce‖ct 拼接）。
// key=env ARB_MASTER_KEY 任意长度口令 → SHA-256 归一 32B；nonce 每密文随机。
// 先例：架构设计 §2.10（enc 列形态）；轮换协议=docs/runbook-秘密轮换.md（D-514 T5）。
package cryptoutil

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"os"
)

// MasterKey ARB_MASTER_KEY 归一化 32B key（空 env → nil,false——fail-closed 由调用方判）。
// D-145 引导件：app_secrets 解密钥放 PG=钥匙锁柜——必须留 env。
func MasterKey() ([]byte, bool) {
	k := os.Getenv("ARB_MASTER_KEY")
	if k == "" {
		return nil, false
	}
	sum := sha256.Sum256([]byte(k))
	return sum[:], true
}

// Seal 加密：返回 nonce‖ciphertext（GCM tag 含在 ct 尾）。
func Seal(key []byte, plaintext string) ([]byte, error) {
	gcm, err := gcmFromKey(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

// Open 解密 nonce‖ct；错 key/篡改 → error（不吐明文）。
func Open(key, blob []byte) (string, error) {
	gcm, err := gcmFromKey(key)
	if err != nil {
		return "", err
	}
	if len(blob) < gcm.NonceSize() {
		return "", fmt.Errorf("ciphertext too short")
	}
	plain, err := gcm.Open(nil, blob[:gcm.NonceSize()], blob[gcm.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(plain), nil
}

func gcmFromKey(key []byte) (cipher.AEAD, error) {
	if len(key) == 0 {
		return nil, fmt.Errorf("empty master key")
	}
	sum := sha256.Sum256(key)
	if len(key) == 32 {
		sum = [32]byte{}
		copy(sum[:], key)
	}
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
