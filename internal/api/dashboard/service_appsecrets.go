// service_appsecrets.go —— D-545 S3：app_secrets 密钥管理面 RPC（ListAppSecrets/UpdateAppSecret）。
// 闸门：密钥值永不回传/入日志（view 只出 has_value/source/hot/updated_at/note；审计行 key-only）。
// 热更：写库后同步 s.secRes.Set/Unset——热读消费点即改即效；未改造键 hot=false 如实标「重启生效」。
package dashboard

import (
	"context"
	"fmt"
	"log"
	"sort"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	dashv1 "nox/internal/gen/dashboardv1"
	"nox/internal/platform/config"
	"nox/internal/platform/cryptoutil"
	"nox/internal/store/pgstore"
)

// hotSecretKeys 热更白名单 v1（D-545 §3）：Get 热读消费点已改造=即改即效；
// 其余密钥可管但 hot=false「重启生效」（构造期绑死键，不伪造热更）。
// D-095：NOX_AUTH_TOKEN 消费点（auth.Interceptor）已改 resolver 热读 → 入列。
var hotSecretKeys = map[string]bool{
	"TRONGRID_API_KEY": true,
	"NOX_AUTH_TOKEN":   true,
}

// SetSecretResolver 注入热读缓存（nil=未装配：List 仍可用，Update 仅写库不 panic）。
func (s *Service) SetSecretResolver(r *config.SecretResolver) { s.secRes = r }

// secretView 键视图：行=DB 行元数据（有行=source db 且 has_value），无行按 resolver
// 回落画 source（D-145：resolver DB∪env 种子统一供值，本包不再直读 env）。
func (s *Service) secretView(key string, rows map[string]pgstore.AppSecretRow) *dashv1.AppSecretKey {
	v := &dashv1.AppSecretKey{Key: key, Group: appSecretGroup(key), Desc: appSecretDescs[key], Hot: hotSecretKeys[key]}
	if r, ok := rows[key]; ok {
		v.HasValue = true
		v.ValueSource = "db"
		v.UpdatedAt = timestamppb.New(r.UpdatedAt)
		v.Note = r.Note
	} else if s.secRes != nil && s.secRes.Get(key) != "" {
		v.HasValue = true
		v.ValueSource = "env"
	} else {
		v.ValueSource = "none"
	}
	return v
}

// ListAppSecrets 全量密钥键视图（遍历白名单种子表；值永不回传）。
func (s *Service) ListAppSecrets(ctx context.Context, _ *connect.Request[dashv1.ListAppSecretsRequest]) (*connect.Response[dashv1.ListAppSecretsResponse], error) {
	dbRows, err := s.store.ListAppSecretRows(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	rows := make(map[string]pgstore.AppSecretRow, len(dbRows))
	for _, r := range dbRows {
		rows[r.Key] = r
	}
	keys := config.AppSecretSeedKeys()
	sorted := make([]string, len(keys))
	copy(sorted, keys)
	sort.Strings(sorted)
	out := make([]*dashv1.AppSecretKey, 0, len(sorted))
	for _, k := range sorted {
		out = append(out, s.secretView(k, rows))
	}
	return connect.NewResponse(&dashv1.ListAppSecretsResponse{Keys: out}), nil
}

// UpdateAppSecret：白名单+note 必填校验→Seal 落库（空值=删行）→resolver 同步→审计行 key-only。
func (s *Service) UpdateAppSecret(ctx context.Context, req *connect.Request[dashv1.UpdateAppSecretRequest]) (*connect.Response[dashv1.UpdateAppSecretResponse], error) {
	key, val, note := req.Msg.GetKey(), req.Msg.GetValue(), req.Msg.GetNote()
	if !config.IsAppSecretKey(key) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("key %s not whitelisted", key))
	}
	if note == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("note required"))
	}
	if val == "" {
		if err := s.store.DeleteAppSecret(ctx, key); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		if s.secRes != nil {
			s.secRes.Unset(key)
		}
		log.Printf("[secrets] %s cleared (source=UpdateAppSecret)", key)
	} else {
		mk, ok := cryptoutil.MasterKey()
		if !ok || mk == nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("主密钥 NOX_MASTER_KEY 未配置，无法写密钥"))
		}
		enc, err := cryptoutil.Seal(mk, val)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		if err := s.store.UpsertAppSecret(ctx, key, enc, note); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		if s.secRes != nil {
			s.secRes.Set(key, val)
		}
		log.Printf("[secrets] %s updated (source=UpdateAppSecret)", key)
	}
	dbRows, err := s.store.ListAppSecretRows(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	rows := make(map[string]pgstore.AppSecretRow, len(dbRows))
	for _, r := range dbRows {
		rows[r.Key] = r
	}
	return connect.NewResponse(&dashv1.UpdateAppSecretResponse{Key: s.secretView(key, rows)}), nil
}
