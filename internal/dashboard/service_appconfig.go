// service_appconfig.go —— D-523：app_config 键管理面 RPC（ListAppConfig/UpdateAppConfig/SelfRestart）。
package dashboard

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"arb/internal/config"
	dashv1 "arb/internal/gen/dashboardv1"
	"arb/internal/store/pgstore"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SetAppConfigView 注入键视图三快照（boot 装配位调用）：running=已 overlay 的进程内 cfg；
// defaults=纯默认 cfg（LoadSansAppConfigKeys）；envSnap=overlay 注入前 env 值表。
func (s *Service) SetAppConfigView(running, defaults *config.Config, envSnap map[string]string) {
	s.acRunning, s.acDefaults, s.acEnvSnap = running, defaults, envSnap
}

func (s *Service) appConfigView(ctx context.Context, key string, db map[string]pgstore.AppConfigRow) (*dashv1.AppConfigKey, error) {
	meta, envPass, ok := config.AppConfigKeyMeta(key)
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("key %s not whitelisted", key))
	}
	row, hasDB := db[key]
	envV, hasEnv := s.acEnvSnap[key]
	out := &dashv1.AppConfigKey{
		Key: key, Type: meta.Typ, Group: config.AppConfigGroup(key),
		EnvValue: envV,
	}
	if hasDB {
		out.DbValue, out.Note = row.Value, row.Note
		out.UpdatedAt = timestamppb.New(row.UpdatedAt)
	}
	var running, def string
	if envPass {
		running = os.Getenv(key) // boot overlay 已把 DB 值注入 env——进程内生效值即 env 现值
	} else {
		running = meta.Get(s.acRunning)
		def = meta.Get(s.acDefaults)
	}
	out.RunningValue, out.DefaultValue = running, def

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
	eff := def
	if hasEnv {
		eff = envV
	}
	if hasDB {
		eff = row.Value
	}
	if envPass {
		out.PendingRestart = eff != running
	} else {
		c2 := *s.acRunning // 浅拷足矣：setter 只写自身字段/换指针
		if err := meta.Set(&c2, eff); err == nil {
			out.PendingRestart = meta.Get(&c2) != running
		}
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
	meta, envPass, ok := config.AppConfigKeyMeta(key)
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
	if !envPass { // 用真 setter 再验一遍（durv/setv 等的解析域严于外形）
		c2 := *s.acRunning
		if err := meta.Set(&c2, val); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid value: %v", err))
		}
	}
	if err := s.store.UpsertAppConfig(ctx, key, val, note); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
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

// SelfRestart D-523 S2 占位（S2 实装自杀延迟件）。
func (s *Service) SelfRestart(ctx context.Context, _ *connect.Request[dashv1.SelfRestartRequest]) (*connect.Response[dashv1.SelfRestartResponse], error) {
	return connect.NewResponse(&dashv1.SelfRestartResponse{}), nil
}
