// rejects_test.go —— D-652 锚：喂已知拒例→对应门 N 断言 + 接线完整性对拍
// （grep 源码 Inc 调用点数 vs Register 门数——注册了不挂/挂了没注册=红）。
package rejects

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestInc_RegisteredGateCounts(t *testing.T) {
	reg.Lock()
	reg.cells = map[Key]*cell{}
	reg.Unlock()
	Register("t_face", "g1", "g2")
	Inc(Key{Face: "t_face", Gate: "g1"})
	Inc(Key{Face: "t_face", Gate: "g1"})
	var g1, g2 uint64
	for _, r := range Snapshot() {
		if r.Face == "t_face" && r.Gate == "g1" {
			g1 = r.N
		}
		if r.Face == "t_face" && r.Gate == "g2" {
			g2 = r.N
		}
	}
	if g1 != 2 || g2 != 0 {
		t.Fatalf("门计数错: g1=%d g2=%d", g1, g2)
	}
}

func TestInc_UnregisteredIgnored(t *testing.T) {
	Inc(Key{Face: "no_face", Gate: "no_gate"}) // 未注册=忽略+warn 一次，不 panic
}

// TestWiringIntegrity 对抗锚（D-652 A.5）：仓内所有 rejects.Inc 调用点
// 的门名 ∈ 同包某 init/调用处的 rejects.Register 注册集——注册了不挂/
// 挂了没注册=红（防「器件在、接线漏」假完成）。
func TestWiringIntegrity(t *testing.T) {
	// 仓内挂点域=internal/tools/cmd（web/gen/vendor 等非 Go 源树不扫——
	// walk 全仓 174s 实测，域外零面零门无贡献）。
	incFaces := map[string][]string{}
	regFaces := map[string]map[string]bool{}
	err := error(nil)
	for _, root := range []string{"../../..", "../../../tools", "../../../cmd"} {
		werr := filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
			if err != nil || fi.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := os.Open(path)
			if err != nil {
				return nil
			}
			defer f.Close()
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				ln := sc.Text()
				if m := regexp.MustCompile(`Face:\s*"([a-z_0-9]+)"`).FindStringSubmatch(ln); m != nil && strings.Contains(ln, "rejects.Inc") {
					incFaces[m[1]] = append(incFaces[m[1]], ln)
				}
				// 行内多 Register 全匹配（同 init 行注册多面是常态）。
				for _, rm := range regexp.MustCompile(`rejects\.Register\(([^)]*)\)`).FindAllStringSubmatch(ln, -1) {
					args := regexp.MustCompile(`"([a-z_0-9]+)"`).FindAllString(rm[1], -1)
					if len(args) < 2 {
						continue
					}
					face := strings.Trim(args[0], `"`)
					if regFaces[face] == nil {
						regFaces[face] = map[string]bool{}
					}
					for _, g := range args[1:] {
						regFaces[face][strings.Trim(g, `"`)] = true
					}
				}
			}
			return nil
		})
		err = werr
	}
	if err != nil {
		t.Fatal(err)
	}
	// Inc 面每门须有注册行。
	incGateRe := regexp.MustCompile(`rejects\.Inc\(rejects\.Key\{Face:\s*"([a-z_0-9]+)",\s*Gate:\s*"([a-z_0-9]+)"`)
	for face, lines := range incFaces {
		for _, ln := range lines {
			m := incGateRe.FindStringSubmatch(ln)
			if len(m) < 3 {
				continue
			}
			if !regFaces[face][m[2]] {
				t.Errorf("挂而未注册: %s.%s", face, m[2])
			}
		}
	}
	// 注册门须有 Inc 挂点（注册了不挂=死键）。
	for face, gates := range regFaces {
		incSet := map[string]bool{}
		for _, ln := range incFaces[face] {
			if m := incGateRe.FindStringSubmatch(ln); len(m) == 3 {
				incSet[m[2]] = true
			}
		}
		for g := range gates {
			if !incSet[g] {
				t.Errorf("注册而未挂: %s.%s", face, g)
			}
		}
	}
}
