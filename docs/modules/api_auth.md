# internal/api/auth

> 事实截至搬迁 HEAD（M2.5，filter-repo 保史）。判定：决策席末节填。

## 职责
ConnectRPC 认证拦截器 + 凭据 AES-GCM 加解密（§13.5）。

## 边界
- **上游**：—（装配点 cmd/core wire 归 M2.6）
- **下游**：connectrpc.com/connect；crypto/aes+cipher+rand（零 internal 依赖）

## 结构
2 个 go 文件（auth.go + auth_test.go）。`ValidateStartupToken`/`NewInterceptor`/AES-GCM 凭据封装。

## 测试
- 测试文件 1；依赖 TestDSN 的 0

## 观察（只记事实）
- 搬迁纯移动+import 路径零改（本包无 internal 依赖）；语义零改动。

## 判定（决策席）
| 项 | 判定 | 理由 |
|---|---|---|
| 包整体 | | |
