// main_test.go —— D-059 ①：DSN 密码段重写单测（URL 形态换密码/user 保留/query 保留；
// keyword= 形态 fail-closed 拒绝）。
package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestRewriteDSNPassword(t *testing.T) {
	user, out, err := rewriteDSNPassword("postgresql://arb_ro:oldpw@127.0.0.1:5433/arb?sslmode=disable", "n3w")
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if user != "arb_ro" {
		t.Fatalf("user 保留: %q", user)
	}
	if out != "postgresql://arb_ro:n3w@127.0.0.1:5433/arb?sslmode=disable" {
		t.Fatalf("out: %q", out)
	}
	// 仅解析（newPw 空）→ 原样返回。
	user, out, err = rewriteDSNPassword("postgresql://arb_ro:oldpw@db:5432/arb", "")
	if err != nil || user != "arb_ro" || out != "postgresql://arb_ro:oldpw@db:5432/arb" {
		t.Fatalf("parse-only: %q %q %v", user, out, err)
	}
	// 无 user 段 → 缺省 arb_ro。
	user, _, err = rewriteDSNPassword("postgresql://127.0.0.1:5433/arb", "x")
	if err != nil || user != "arb_ro" {
		t.Fatalf("default user: %q %v", user, err)
	}
	// keyword= 形态：url.Parse 出空 authority——fail-closed 拒。
	if _, _, err := rewriteDSNPassword("host=arb port=5433 user=arb_ro dbname=arb", "x"); err == nil {
		t.Fatal("keyword= 形态应拒绝")
	}
}

// TestRedactErr_ParseNoEcho D-059 R1 退回单锚：parse 类错误透输入原文——
// 含密码的坏 DSN 经 rewriteDSNPassword/verifyAddrRewrite 返回的错误文本必须零回显
// （url.Parse 对坏转义报 `parse "<原文>": ...`，原文嵌入错误串）。
func TestRedactErr_ParseNoEcho(t *testing.T) {
	const bad = "postgresql://arb_ro:sup3rs3cret@127.0.0.1:5433/%zz"
	_, _, err := rewriteDSNPassword(bad, "")
	if err == nil {
		t.Fatal("坏转义 DSN 应报错")
	}
	if strings.Contains(err.Error(), "sup3rs3cret") || strings.Contains(err.Error(), bad) {
		t.Fatalf("DSN 原文/密码回显进错误: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "***") {
		t.Fatalf("应含打码标记: %q", err.Error())
	}
	// verifyAddrRewrite 同路径。
	_, err = verifyAddrRewrite(bad, "127.0.0.1:5433")
	if err == nil {
		t.Fatal("verify 路径坏 DSN 应报错")
	}
	if strings.Contains(err.Error(), "sup3rs3cret") || strings.Contains(err.Error(), bad) {
		t.Fatalf("verify 路径回显: %q", err.Error())
	}
	// redactErr 直测：多秘密替换。
	got := redactErr(fmt.Errorf(`parse "A" and "B" failed`), "A", "B")
	if strings.Contains(got, "A") || strings.Contains(got, "B") || !strings.Contains(got, "***") {
		t.Fatalf("redactErr: %q", got)
	}
}
