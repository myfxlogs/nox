// evidence.go —— 证据泛化层：protoreflect 顶层非空字段集+protojson 摘录+
// gRPC reflection 全 dump（service/method/消息字段/枚举值——补「名单 grep」缺口）
// +产物直出（JSON schema 固定+MD 表——禁手编）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// isZeroValue 字段零值判定（protoreflect Equal 与消息类型比较）。
func isZeroValue(m proto.Message, fd protoreflect.FieldDescriptor) bool {
	v := m.ProtoReflect().Get(fd)
	zero := m.ProtoReflect().New().Get(fd)
	return v.Equal(zero)
}

var _ = fmt.Sprintf
var _ = strings.HasPrefix

// rawStore 原始响应全文存证（防做假①：raw JSON 存证不止摘要——key=rpc@venue）。
var rawStore = map[string]string{}

// recordRaw 全量 protojson 落 rawStore（writeOut 时直出 raw-<day>.json）。
func recordRaw(key string, m proto.Message) {
	if m == nil {
		return
	}
	b, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(m)
	if err == nil {
		rawStore[key] = sanitizePII(string(b))
	}
}

// keyFields 顶层非空字段集（零值字段不计——「关键字段非空集」票面语义）。
func keyFields(m proto.Message) []string {
	if m == nil {
		return nil
	}
	var out []string
	md := m.ProtoReflect().Descriptor()
	fds := md.Fields()
	for i := 0; i < fds.Len(); i++ {
		fd := fds.Get(i)
		if !isZeroValue(m, fd) {
			out = append(out, string(fd.Name()))
		}
	}
	sort.Strings(out)
	return out
}

// piiFields PII 字段集（Account 面户主信息——脱敏后入证据，复审㉚伤①）。
var piiFields = map[string]bool{
	"UserName": true, "Email": true, "Phone": true, "City": true,
	"Country": true, "State": true, "ZipCode": true, "UserAddress": true,
}

// sanitizePII protojson 串内 PII 字段值置换 ***（值形态 "field":"value"）。
func sanitizePII(s string) string {
	for f := range piiFields {
		i := strings.Index(s, `"`+f+`":`)
		if i < 0 {
			continue
		}
		vs := strings.Index(s[i:], ":") + i + 1
		ve := strings.IndexByte(s[vs:], ',')
		if ve < 0 {
			ve = len(s)
		} else {
			ve += vs
		}
		s = s[:vs] + `"***"` + s[ve:]
	}
	return s
}

// protojsonish protojson 摘录（前 220 字节，PII 脱敏）。
func protojsonish(m proto.Message) string {
	if m == nil {
		return ""
	}
	b, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(m)
	if err != nil {
		return ""
	}
	return truncStr(sanitizePII(string(b)))
}

// ReflectionDump reflection 全量 dump（service→methods→输入消息字段+枚举）。
type ReflectionDump struct {
	Services map[string]ReflectionService `json:"services"`
	Enums    map[string][]string          `json:"enums"`
	Messages map[string][]string          `json:"messages"`
}

type ReflectionService struct {
	Methods []string `json:"methods"`
}

// reflectDump 经 grpc reflection（ServerReflectionInfo bidi）拉服务/符号清单。
// 失败=reflection:{error}（如实记，不阻塞主探针）。
func reflectDump(gconn *grpc.ClientConn) ReflectionDump {
	out := ReflectionDump{Services: map[string]ReflectionService{}, Enums: map[string][]string{}, Messages: map[string][]string{}}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// reflection 走独立流（ServerReflectionInfo bidi stream）——简版：ListServices。
	rc := mt5ReflectionClient{conn: gconn}
	svcs, err := rc.listServices(ctx)
	if err != nil {
		out.Services["reflection-error"] = ReflectionService{Methods: []string{err.Error()}}
		return out
	}
	for _, s := range svcs {
		methods, err := rc.listMethods(ctx, s)
		if err != nil {
			out.Services[s] = ReflectionService{Methods: []string{err.Error()}}
			continue
		}
		out.Services[s] = ReflectionService{Methods: methods}
		// 消息/枚举：拉每方法的输入消息定义（file contains symbol）。
		for _, mn := range methods {
			msgName := methodNameToInput(s, mn)
			if _, done := out.Messages[msgName]; done {
				continue
			}
			fields, enums, err := rc.describeSymbol(ctx, msgName)
			if err == nil {
				out.Messages[msgName] = fields
				for en, vals := range enums {
					out.Enums[en] = vals
				}
			}
		}
	}
	return out
}

// writeOut JSON+MD 直出。
func writeOut(dir string, evs []Evidence, refl ReflectionDump) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}
	day := time.Now().UTC().Format("2006-01-02")
	doc := map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"gateway":      "mt5grpc.mtapi.io:443",
		"evidence":     evs,
		"reflection":   refl,
	}
	jb, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		log.Fatalf("json: %v", err)
	}
	jp := filepath.Join(dir, fmt.Sprintf("mt5-rpc-probe-%s.json", day))
	if err := os.WriteFile(jp, jb, 0o644); err != nil {
		log.Fatalf("write json: %v", err)
	}
	var b strings.Builder
	b.WriteString("# mt5 RPC 实测能力账（harness 直出禁手编）\n\n")
	b.WriteString(fmt.Sprintf("生成：%s｜网关：mt5grpc.mtapi.io:443｜行数：%d\n\n", time.Now().UTC().Format(time.RFC3339), len(evs)))
	b.WriteString("| service.rpc | venue | status | ms | errcode | keyfields |\n|---|---|---|---|---|---|\n")
	for _, e := range evs {
		b.WriteString(fmt.Sprintf("| %s.%s | %s | %s | %d | %s | %s |\n",
			e.Service, e.RPC, e.Venue, e.Status, e.MS, truncStr(e.ErrCode), strings.Join(e.KeyFields, ",")))
	}
	b.WriteString("\n## reflection 全量\n\n")
	for s, rs := range refl.Services {
		b.WriteString(fmt.Sprintf("- **%s**: %s\n", s, strings.Join(rs.Methods, ", ")))
	}
	// 防做假①：raw 全文存证（每 RPC@venue 完整响应 JSON——与摘要双件）。
	rb, err := json.MarshalIndent(rawStore, "", "  ")
	if err == nil {
		rp := filepath.Join(dir, fmt.Sprintf("mt5-rpc-raw-%s.json", day))
		if werr := os.WriteFile(rp, rb, 0o644); werr == nil {
			log.Printf("[mtprobe] raw written: %s (%d keys)", rp, len(rawStore))
		}
	}
	// 防做假③：覆盖机械对账（reflection 全方法 vs evidence 覆盖集——缺项直出）。
	covered := map[string]bool{}
	for _, e := range evs {
		covered[e.RPC] = true
	}
	var missing []string
	for svcName, rs := range refl.Services {
		for _, mn := range rs.Methods {
			base := strings.TrimPrefix(mn, "On")
			if !covered[mn] && !covered[base] && !strings.HasSuffix(mn, "Reply") {
				missing = append(missing, svcName+"."+mn)
			}
		}
	}
	sort.Strings(missing)
	b.WriteString("\n## 覆盖对账（reflection 全方法 vs 实测集）\n\n")
	if len(missing) == 0 {
		b.WriteString("全覆盖：reflection 清单无缺项。\n")
	} else {
		b.WriteString(fmt.Sprintf("缺 %d 项：\n", len(missing)))
		for _, m := range missing {
			b.WriteString("- " + m + "\n")
		}
	}
	mp := filepath.Join(dir, fmt.Sprintf("mt5-rpc-probe-%s.md", day))
	if err := os.WriteFile(mp, []byte(b.String()), 0o644); err != nil {
		log.Fatalf("write md: %v", err)
	}
	log.Printf("[mtprobe] evidence written: %s + %s", jp, mp)
}
