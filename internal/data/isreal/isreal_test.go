package isreal

import "testing"

func TestPair_BothKnownSame(t *testing.T) {
	t1 := true
	t2 := true
	m := map[string]*bool{"vA": &t1, "vB": &t2}
	if !Pair(m, "vA", "vB", true) {
		t.Error("both true → should pair (failOpen=true)")
	}
	if !Pair(m, "vA", "vB", false) {
		t.Error("both true → should pair (failOpen=false)")
	}
}

func TestPair_BothKnownDifferent(t *testing.T) {
	t1 := true
	f1 := false
	m := map[string]*bool{"vA": &t1, "vB": &f1}
	if Pair(m, "vA", "vB", true) {
		t.Error("true×false → should not pair (failOpen=true)")
	}
	if Pair(m, "vA", "vB", false) {
		t.Error("true×false → should not pair (failOpen=false)")
	}
}

func TestPair_AMissing(t *testing.T) {
	b := true
	m := map[string]*bool{"vB": &b}
	// A 缺失：failOpen=true → true（放行）；failOpen=false → false（不配对）
	if !Pair(m, "vA", "vB", true) {
		t.Error("A missing + failOpen=true → should pair (放行)")
	}
	if Pair(m, "vA", "vB", false) {
		t.Error("A missing + failOpen=false → should not pair")
	}
}

func TestPair_NilValue(t *testing.T) {
	var nilPtr *bool
	b := true
	m := map[string]*bool{"vA": nilPtr, "vB": &b}
	// A=nil：failOpen=true → true；failOpen=false → false
	if !Pair(m, "vA", "vB", true) {
		t.Error("A nil + failOpen=true → should pair (放行)")
	}
	if Pair(m, "vA", "vB", false) {
		t.Error("A nil + failOpen=false → should not pair")
	}
}

func TestPair_EmptyMap(t *testing.T) {
	// 空 map 同缺失
	if !Pair(map[string]*bool{}, "vA", "vB", true) {
		t.Error("empty map + failOpen=true → should pair (放行)")
	}
	if Pair(map[string]*bool{}, "vA", "vB", false) {
		t.Error("empty map + failOpen=false → should not pair")
	}
}

func TestPair_BothFalse(t *testing.T) {
	f1 := false
	f2 := false
	m := map[string]*bool{"vA": &f1, "vB": &f2}
	if !Pair(m, "vA", "vB", true) {
		t.Error("both false → should pair (failOpen=true)")
	}
	if !Pair(m, "vA", "vB", false) {
		t.Error("both false → should pair (failOpen=false)")
	}
}
