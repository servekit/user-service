# Identity 包重构 + tencent/mini 迁移 设计

## 背景与动机

当前 `internal/identity/` 包把以下内容混在一个包里：

- 公共接口（`SocialProvider`、`RedirectProvider`、`SocialResult`）
- 密码哈希工具（`HashPassword`、`VerifyPassword`）
- 五个 provider 实现（GitHub、Google、WeChat Web、WeChat MiniProgram、Apple）
- `go-common/tencent/mini` 是 `WeChatMiniProgramProvider` 的底层依赖，独立维护在 go-common 仓库

问题：

1. **go-common 中的 mini 维护成本高**：每次扩展（如新增 API、调整缓存策略）都要发 go-common 版本，user-service 升级依赖又涉及 replace 更新。收益不高，反而拖慢迭代。
2. **identity 包职责过多**：接口、工具、五种 provider 实现全堆在一起，找代码、加新 provider 时都要在一个大包里翻。

## 目标

1. **迁移 mini 包**：把 `github.com/servekit/go-common/tencent/mini` 整体搬到本项目，去掉对 go-common/tencent/mini 的依赖。
2. **重构 identity 包**：抽公共接口/类型到 `provider` 子包；每个 provider 实现独立成自己的子包；密码工具留在根包。

## 非目标

- 不修改任何 provider 的业务逻辑（除了包路径、类型名）。
- 不调整 `slog` 用法、不引入新依赖、不重构 mini 的缓存/singleflight 实现。
- 不动 OAuth 流程、session、RBAC、proto 定义。

## 目录结构

```
internal/identity/
├── credentials.go                 # 保留原位（密码哈希工具）
├── provider/
│   └── provider.go                # SocialProvider/RedirectProvider/SocialResult
├── github/
│   └── github.go                  # Provider
├── google/
│   └── google.go                  # Provider
├── apple/
│   └── apple.go                   # Provider（占位，未实现）
└── tencent/
    ├── wechat/
    │   └── wechat.go              # Provider（微信 Web QR + extraString helper）
    └── mini/
        ├── types.go               # Config/LoginResp/AccessTokenResp/PhoneNumberResp/PhoneInfo
        ├── client.go              # API client
        ├── manager.go             # 多 appid + token 缓存
        ├── provider.go            # WeChatMiniProgramProvider（重命名为 mini.Provider）
        ├── client_test.go
        └── manager_test.go
```

`tencent/` 子树归集所有腾讯系登录方式（微信扫码、微信小程序）。未来如要加企业微信、QQ 等，可一并放入此子树。

## 命名约定

| 旧名 | 新名 |
|------|------|
| `identity.SocialProvider` | `provider.SocialProvider` |
| `identity.RedirectProvider` | `provider.RedirectProvider` |
| `identity.SocialResult` | `provider.SocialResult` |
| `identity.GitHubProvider` / `identity.NewGitHubProvider` | `github.Provider` / `github.New` |
| `identity.GoogleProvider` / `identity.NewGoogleProvider` | `google.Provider` / `google.New` |
| `identity.WeChatProvider` / `identity.NewWeChatProvider` | `wechat.Provider` / `wechat.New`（路径 `tencent/wechat`） |
| `identity.AppleProvider` / `identity.NewAppleProvider` | `apple.Provider` / `apple.New` |
| `identity.WeChatMiniProgramProvider` / `identity.NewWeChatMiniProgramProvider` | `mini.Provider` / `mini.NewProvider` |
| `identity.HashPassword` / `identity.VerifyPassword` | 不变（留根包） |

注：`mini` 包内部已有 `Client`、`Manager` 等类型，再加 `New` 容易混淆，故 provider 构造函数命名为 `mini.NewProvider(appID, mgr)`。

## mini 包内部改动

- **import 路径**：从 `github.com/servekit/go-common/tencent/mini` 改为 `user-service/internal/identity/tencent/mini`。
- **包文档**：保留 `// Package mini provides a WeChat Mini Program API client with access token management.`，可补一句"包含 SocialProvider 实现"。
- **manager.go 中的 `slog.Error`**：1:1 迁移，本 PR 保留原样。后续 PR 按 CLAUDE.md 规范化（库不打日志）。
- **`WeChatMiniProgramProvider` → `Provider`**：与其它 provider 命名对齐。

## extraString helper

只被 `wechat.go` 使用（取 `openid`、`unionid`），挪到 `internal/identity/tencent/wechat/wechat.go` 内作为 unexported helper。

## 使用方调整清单

### `internal/service/service.go`

- 新增 imports：`user-service/internal/identity/provider`、`user-service/internal/identity/github`、`google`、`apple`、`user-service/internal/identity/tencent/wechat`、`user-service/internal/identity/tencent/mini`
- 移除：`github.com/servekit/go-common/tencent/mini`
- map 类型：`map[pb.IdentityProvider]identity.SocialProvider` → `map[pb.IdentityProvider]provider.SocialProvider`
- 各构造函数调用相应子包的 `New(...)` / `mini.NewProvider(...)`

### `internal/service/social/social.go`

- `identity.SocialProvider` / `RedirectProvider` / `SocialResult` → `provider.Xxx`
- 类型断言 `*identity.WeChatMiniProgramProvider` → `*mini.Provider`

### `internal/service/auth/auth.go`、`internal/service/user/user.go`

- 不变（`identity.HashPassword` / `VerifyPassword` 留根包）

## 边界与一致性

- **包名冲突**：`golang.org/x/oauth2/github` 和 `user-service/internal/identity/github` 都叫 `github`；`golang.org/x/oauth2/google` 同理。service.go 不直接 import 这些 oauth2 子包（它们只在各 provider 子包内部使用），无冲突。
- **循环依赖**：各 provider 子包 → `provider` 子包（取接口/类型）；`provider` 子包不依赖任何 provider 子包；`tencent/wechat` 和 `tencent/mini` 同样依赖 `provider`。无环。`tencent/` 不是 Go 包目录（无 .go 文件），仅作分类。
- **`apple.Provider` 未实现**：保留占位行为，service.go 仍然注册它（调用方拿到 `not implemented` 错误）。

## 验证

```bash
gofmt -w .
goimports -w .
golangci-lint run ./...
go test -race ./internal/identity/... ./internal/service/...
go build ./...
```

## 分阶段交付

1. 新建子包目录与文件（copy + 调整包名/类型名）。
2. 移除旧的 `internal/identity/{github,google,wechat,apple,wechat_miniprogram,provider}.go`。
3. 更新使用方 import 与类型引用。
4. 跑测试 + lint + build。

## 关联

- 实现计划：[[services/user-service/plan/v1/1-identity-refactor|1-identity-refactor]]（writing-plans 阶段产出）
