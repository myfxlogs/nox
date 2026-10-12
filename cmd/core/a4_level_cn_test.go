package main

// d573_level_test.go —— D-573 验收锚 A4：飞书前缀 levelCN 纯展示映射。

import "testing"

func TestD573_A4_LevelCN(t *testing.T) {
	cases := map[string]string{
		"warn": "警告", "warning": "警告", "critical": "严重",
		"info": "提示", "error": "错误", "weird-x": "weird-x", "": "",
	}
	for in, want := range cases {
		if got := levelCN(in); got != want {
			t.Fatalf("levelCN(%q)=%q want %q", in, got, want)
		}
	}
}
