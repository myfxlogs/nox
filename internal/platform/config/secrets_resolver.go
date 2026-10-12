// secrets_resolver.go —— D-545：密钥热读缓存（antclaw CredentialResolver 模式单进程化）。
// boot 种子=LoadAppSecrets 解密集 ∪ env 回落（DB>env 同 overlay 语义）；
// UpdateAppSecret RPC 写库后同步 Set/Unset——消费点 Get() 热读=即改即效。
// 白名单外键 Get 恒 ""（fail-closed，防跨域键复用静默 miss——KB cross-domain-key-reuse-silent-miss）。
// 双轨注释：env 直读点未改造完前 ApplyAppSecretsOverlay 仍保留（构造期绑死键=重启生效）。
package config

import (
	"sync"
)

// secretResolverAllowed resolver 可读键集=密钥白名单 ∪ 借轨标量（NOX_MACRO_ENABLED
// 热闸借同一缓存轨，非密钥但同「写后同步 Set 即生效」语义——D-545 §2）。
func secretResolverAllowed(k string) bool {
	return IsAppSecretKey(k) || k == "NOX_MACRO_ENABLED"
}

// SecretResolver 密钥热读缓存：Get 为消费点唯一读口；Set/Unset 为写面（RPC 同步）。
type SecretResolver struct {
	mu   sync.RWMutex
	vals map[string]string
}

// NewSecretResolver 构造（种子=DB 解密集；env 回落由装配方显式 Set 注入，保持本件零 env 依赖）。
func NewSecretResolver(secrets map[string]string) *SecretResolver {
	r := &SecretResolver{vals: make(map[string]string, len(secrets))}
	for k, v := range secrets {
		if secretResolverAllowed(k) && v != "" {
			r.vals[k] = v
		}
	}
	return r
}

// Get 消费点读口（nil 安全：未装配返回 ""=fail-closed；白名单外/空值= ""）。
func (r *SecretResolver) Get(key string) string {
	if r == nil || !secretResolverAllowed(key) {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.vals[key]
}

// Set 写入同步（nil/白名单外/空值走 Unset 语义——空串等价清除）。
func (r *SecretResolver) Set(key, value string) {
	if r == nil || !secretResolverAllowed(key) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if value == "" {
		delete(r.vals, key)
		return
	}
	r.vals[key] = value
}

// Unset 清行同步（写空值路径）。
func (r *SecretResolver) Unset(key string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.vals, key)
	r.mu.Unlock()
}

// Has 键是否有有效值（nil/白名单外=false）。
func (r *SecretResolver) Has(key string) bool { return r.Get(key) != "" }
