// service_appconfig.go —— D-523：app_config 键管理面 RPC（ListAppConfig/UpdateAppConfig/SelfRestart）。
package dashboard

import (
	"context"
	"fmt"
	"strconv"
	"syscall"
	"time"

	dashv1 "arb/internal/gen/dashboardv1"
	"arb/internal/platform/config"
	"arb/internal/store/pgstore"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SetAppConfigView 注入键视图三快照（boot 装配位调用）：running=已 overlay 的进程内 cfg；
// defaults=纯默认 cfg（LoadSansAppConfigKeys）；envSnap=overlay 注入前 env 值表。
// appConfigView D-523 键视图快照 + SelfRestart 注入件。
type appConfigView struct {
	running, defaults *config.Config
	envSnap           map[string]string
	kill              func(int, syscall.Signal) error
	delay             time.Duration
}

func (s *Service) SetAppConfigView(running, defaults *config.Config, envSnap map[string]string) {
	s.acv.running, s.acv.defaults, s.acv.envSnap = running, defaults, envSnap
}

func (s *Service) appConfigView(ctx context.Context, key string, db map[string]pgstore.AppConfigRow) (*dashv1.AppConfigKey, error) {
	meta, ok := config.AppConfigKeyMeta(key)
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("key %s not whitelisted", key))
	}
	row, hasDB := db[key]
	envV, hasEnv := s.acv.envSnap[key]
	out := &dashv1.AppConfigKey{
		Key: key, Type: meta.Typ, Group: config.AppConfigGroup(key), Desc: meta.Desc,
		GroupLabel: config.AppConfigGroupLabel(config.AppConfigGroup(key)),
		EnvValue:   envV,
	}
	if hasDB {
		out.DbValue, out.Note = row.Value, row.Note
		out.UpdatedAt = timestamppb.New(row.UpdatedAt)
	}
	running := meta.Get(s.acv.running)
	out.RunningValue, out.DefaultValue = running, meta.Get(s.acv.defaults)

	// valueSource = boot 优先级链 db>env>default。
	switch {
	case hasDB:
		out.ValueSource = "db"
	case hasEnv:
		out.ValueSource = "env"
	default:
		out.ValueSource = "default"
	}

	// pendingRestart：重启后生效值（db??env??default 解析后）≠ 进程内 running。
	eff := out.DefaultValue
	if hasEnv {
		eff = envV
	}
	if hasDB {
		eff = row.Value
	}
	c2 := *s.acv.running // 浅拷足矣：setter 只写自身字段/换指针
	if err := meta.Set(&c2, eff); err == nil {
		out.PendingRestart = meta.Get(&c2) != running
	}
	return out, nil
}

func (s *Service) ListAppConfig(ctx context.Context, _ *connect.Request[dashv1.ListAppConfigRequest]) (*connect.Response[dashv1.ListAppConfigResponse], error) {
	rows, err := s.store.ListAppConfigRows(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	db := make(map[string]pgstore.AppConfigRow, len(rows))
	for _, r := range rows {
		db[r.Key] = r
	}
	resp := &dashv1.ListAppConfigResponse{}
	for _, k := range config.AppConfigAllKeys() {
		v, err := s.appConfigView(ctx, k, db)
		if err != nil {
			return nil, err
		}
		resp.Keys = append(resp.Keys, v)
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) UpdateAppConfig(ctx context.Context, req *connect.Request[dashv1.UpdateAppConfigRequest]) (*connect.Response[dashv1.UpdateAppConfigResponse], error) {
	key, val, note := req.Msg.GetKey(), req.Msg.GetValue(), req.Msg.GetNote()
	meta, ok := config.AppConfigKeyMeta(key)
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("key %s not whitelisted", key))
	}
	if note == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("note required"))
	}
	switch meta.Typ {
	case "bool":
		if val != "true" && val != "false" {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("bool value must be true|false"))
		}
	case "number":
		if _, err := strconv.ParseFloat(val, 64); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("number value unparsable"))
		}
	default:
		if val == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("string value must be non-empty"))
		}
	}
	// 用真 setter 再验一遍（durv/setv 等的解析域严于外形）
	c2 := *s.acv.running
	if err := meta.Set(&c2, val); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid value: %v", err))
	}
	if err := s.store.UpsertAppConfig(ctx, key, val, note); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// D-545：ARB_MACRO_ENABLED 借密钥轨热推——写库成功即同步 resolver，当班闸即效免重启。
	if key == "ARB_MACRO_ENABLED" && s.secRes != nil {
		s.secRes.Set(key, val)
	}
	// D-093 R1：出场费率=app_config 明文热轨——写库成功即热换 running 视图（scoped 本键，
	// 不动其他键 pendingRestart 语义），enrich 下一轮按新费率，免重启。
	if key == "ARB_EXIT_FEE_BPS_OKX" && s.acv.running != nil {
		c2 := *s.acv.running
		if err := meta.Set(&c2, val); err == nil {
			s.acv.running = &c2
		}
	}
	rows, err := s.store.ListAppConfigRows(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	db := make(map[string]pgstore.AppConfigRow, len(rows))
	for _, r := range rows {
		db[r.Key] = r
	}
	v, err := s.appConfigView(ctx, key, db)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&dashv1.UpdateAppConfigResponse{Key: v}), nil
}

// SetSelfRestart 注入自杀件（测试换 fake；装配位 nil=默认 syscall.Kill+2s）。
// 禁 os.Exit（绕过 defer 清理）——SIGTERM 复用既有优雅退出链，compose unless-stopped 拉起。
func (s *Service) SetSelfRestart(kill func(int, syscall.Signal) error, delay time.Duration) {
	s.acv.kill, s.acv.delay = kill, delay
}

// SelfRestart：响应先回（RPC 200 已发出）→ delay 后 SIGTERM 自杀。
func (s *Service) SelfRestart(ctx context.Context, _ *connect.Request[dashv1.SelfRestartRequest]) (*connect.Response[dashv1.SelfRestartResponse], error) {
	kill, delay := s.acv.kill, s.acv.delay
	if kill == nil {
		kill = syscall.Kill
	}
	if delay == 0 {
		delay = 2 * time.Second
	}
	go func() {
		time.Sleep(delay)
		_ = kill(syscall.Getpid(), syscall.SIGTERM)
	}()
	return connect.NewResponse(&dashv1.SelfRestartResponse{}), nil
}
