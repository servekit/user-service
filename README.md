# user-service

通用用户系统服务:多登录方式(邮箱 / 手机 / OAuth2 / 微信小程序)、Session 有状态管理、RBAC 权限。

支持两种部署形态:

- **独立 gRPC 服务** —— 监听 `:19094`(gRPC)。**纯 gRPC 服务**,不监听 HTTP;对外 HTTP 面由网关(当前为 testkit-service)提供
- **Go module in-process** —— 在父进程内调用 `service.New(cfg)` 拿到 `*Service`,直接调方法,不经过网络

---

## 目录

- [依赖](#依赖)
- [构建与运行](#构建与运行)
- [典型流程](#典型流程)
  - [注册(邮箱 / 手机)](#注册邮箱--手机)
  - [登录](#登录)
  - [OAuth 重定向登录](#oauth-重定向登录)
  - [微信小程序登录](#微信小程序登录)
  - [密码重置(忘记密码)](#密码重置忘记密码)
  - [管理员建号 + 激活](#管理员建号--激活)
  - [Session 管理](#session-管理)
  - [RBAC 配置](#rbac-配置)
- [作为 Go Module 使用](#作为-go-module-使用)
- [错误码](#错误码)
- [未实现的接口](#未实现的接口)

---

## 依赖

| 组件 | 用途 | 说明 |
|---|---|---|
| PostgreSQL | 持久化(用户、identity、session、登录日志、RBAC 表) | 通过 `dbx.New` 初始化 |
| Redis | Session 状态、验证码(captcha)、登录限流、RBAC 缓存 | 通过 `redisx.New` 初始化 |
| message-service | 发送验证码邮件 / 短信 | gRPC 调用,模板内容由调用方提供 |
| gid-service | 雪花算法 ID 生成 | 通过 gRPC 获取 user_id / group_id / role_id |
| OAuth providers | GitHub / Google / WeChat / Apple / WeChat MiniProgram | 配置 OAuth.* 字段 |

---

## 构建与运行

服务与数据库迁移合并为同一个二进制(`cmd/server`),通过子命令区分:

| 命令 | 作用 |
|---|---|
| `./user-service` 或 `./user-service serve` | 启动 gRPC 服务(默认) |
| `./user-service migrate` | 执行 GORM AutoMigrate 后退出 |
| 其他 | 打印用法,exit 2 |

本地开发:

```bash
make build      # 产出 bin/user-service
make run        # go run 启动服务
make migrate    # go run 执行迁移
```

迁移与发版解耦 —— 先单独跑迁移,再启动服务(启动时**不再自动迁移**):

```bash
./user-service migrate     # CI / 部署脚本手动执行一次
./user-service             # 启动服务
```

> 二进制名由 Makefile 的 `BIN_NAME` 变量管理(默认 `user-service`),改名只需覆盖该变量;Go 包路径固定为 `cmd/server`。user-service 暂无 Dockerfile,镜像构建待补。

---

## 典型流程

### 注册(邮箱 / 手机)

`SendVerificationCode` → `Register`

```
1. SendVerificationCode(purpose=REGISTER, channel=EMAIL/SMS, ...)
   → 返回 captcha_id,验证码经 message-service 发到用户邮箱/手机
2. Register(provider=EMAIL/PHONE, code, captcha_id, password, ...)
   → 服务端用 captcha_id 校验码是否匹配 → 建用户 + identity + session
   → 返回 user + session_id(已登录)
```

注意:

- `captcha_id` 必须传回,验证码绑定生成它的流程,防止跨上下文重放
- `password` 必填,会 bcrypt 哈希后存到 identity 的 credentials 字段
- 邮箱/手机已存在时返回 `ErrIdentityExists`

### 登录

`Login` 根据 `method` 自动路由,五种方法分两组:

| method | 凭证 | 自动注册 | 需要 SendVerificationCode |
|---|---|---|---|
| `USERNAME_PASSWORD` | 用户名 + 密码 | 否 | 否 |
| `EMAIL_PASSWORD` | email + 密码 | 否 | 否 |
| `PHONE_PASSWORD` | region_code+phone + 密码 | 否 | 否 |
| `EMAIL_CODE` | email + code + captcha_id | **是**(找不到 identity 时) | **是**(purpose=LOGIN) |
| `PHONE_CODE` | region_code+phone + code + captcha_id | **是** | **是** |

返回 `LoginResponse{user, session_id, is_new}`。`is_new=true` 表示验证码登录触发了自动注册。

限流:`loginLimiter` 按 lookup target 限频(默认 5 分钟 5 次失败、1 小时 15 次失败)。

### OAuth 重定向登录(统一回调架构)

> **部署提示(先看这条)**:
>
> 1. user-service 是 **gRPC-only**,**不包含** OAuth 提供方回调的 HTTP 服务
> 2. 该 HTTP 回调服务由**网关团队 / 业务方**负责独立搭建(下文有 50 行 demo 参考,**不能直接拿去上生产**)
> 3. 配置项 `cfg.OAuth.{provider}.RedirectURL` 必须指向该回调服务的**公网地址**( OAuth 提供方后台注册什么就填什么,精确到 scheme / 末尾斜杠)
> 4. 生产部署需要额外做:**错误页**(不要直接把 err 字符串返给用户)、**限流**(回调端点防滥用)。token 安全方面,见下文 [BFF 安全契约](#bff-安全契约must) —— session_id 不允许出现在 URL 里,MUST 用一次性短 code 兑换模式。

user-service 是 gRPC-only,**不直接接收 OAuth 提供方的 HTTP 回调**。多业务共用一套 OAuth App 时,采用"统一回调服务"架构:OAuth 提供方注册的回调地址是**唯一**的(属于那个统一回调服务),业务方通过 `return_to` 告知 user-service"用户最后要回哪"。

**架构角色:**

| 角色 | 职责 | 是否本服务 |
|------|------|----------|
| 业务方 (BFF) | 调 `GetOAuthURL`、把用户跳到授权页、最后从 `return_to` 拿 session token | 否 |
| 统一回调服务 | 拥有 OAuth 注册的固定回调 URL、收 `code+state`、调 `SocialLogin`、302 跳业务方 | **否,需另行搭建** |
| user-service | 校验 state、ExchangeCode、找/建用户、开 session、返回 `return_to` | 是 |
| OAuth 提供方 | 授权、回调固定 URL 带 `code+state` | 否 |

**流程:**

```
浏览器           业务方 (a.com)        统一回调服务          user-service        GitHub
  │                  │               (auth.corp.com)                            │
  │ ① 点登录          │                  │                   │                   │
  │ ────────────>    │                  │                   │                   │
  │                  │ ② GetOAuthURL(return_to=a.com/done)   │                   │
  │                  │ ────────────────────────────────────> │                   │
  │                  │                   │                   │                   │
  │                  │    user-service 生成 state,Redis 里存:                   │
  │                  │    state → { provider=GITHUB, return_to=a.com/done }     │
  │                  │                   │                   │                   │
  │                  │ <─ GitHub 授权 URL(redirect_uri=cfg.OAuth.GitHub.RedirectURL, state=xxx) │
  │                  │                   │                   │                   │
  │ ③ 302 跳 GitHub  │                   │                   │                   │
  │ <────────────    │                   │                   │                   │
  │                                                                                  │
  │ ④ 用户在 GitHub 授权                                                              │
  │ ────────────────────────────────────────────────────────────────────────────> │
  │                                                                                  │
  │ ⑤ GitHub 302 跳到统一回调服务的固定 URL                                           │
  │   https://auth.corp.com/oauth/callback?code=...&state=...                        │
  │ <───────────────────────────────────────────────────────────────────────────── │
  │                                                                                  │
  │ ⑥ 统一回调服务收到回调                                                            │
  │ ─────────────────────────>                                                      │
  │                          │ ⑦ 调 SocialLogin(code, state) ──────────>  user-service
  │                          │                       │                   │           │
  │                          │                       │ 校验 state                   │
  │                          │                       │ ExchangeCode 拿 GitHub UID   │
  │                          │                       │ 找/建用户 + 开 session       │
  │                          │                       │ <── { session_id, return_to }│
  │                          │                       │                   │           │
  │ ⑧ 统一回调服务 302 跳到业务方的 return_to                                          │
  │   a.com/done?code=<one-time short code>  (用 IssueSessionCode 兑换)              │
  │ <─────────────────────────│                                                      │
  │                                                                                  │
  │ ⑨ 浏览器请求 a.com/done?code=xxx                                                 │
  │ ─────────────────>                                                              │
  │                  │                                                                │
  │                  │ 业务方从 URL/cookie 拿到 session_id,后续请求带这个 token     │
```

**关键:**

- `cfg.OAuth.{provider}.RedirectURL` —— OAuth 提供方后台注册的固定回调 URL,**属于统一回调服务**,不属于任何业务方
- `cfg.OAuth.{provider}.AllowedRedirectURLs` —— 业务方 `return_to` 白名单(精确匹配),**默认拒绝**(空 + 非空 return_to → 直接拒绝;详见下文 BFF 安全契约)
- `GetOAuthURLRequest.return_to` —— 业务回跳地址,会编码到 state 里
- `LoginResponse.return_to` —— user-service 在响应里把它带回来,让统一回调服务知道跳哪

#### BFF 安全契约(MUST)

`SocialLogin` 防得住"假 state",防不住"真 state 被偷换"。下面的契约 BFF 必须遵守,否则会有 Login CSRF 风险(攻击者把自己的 code 喂进受害者的 state,让受害者登入攻击者账号):

1. **state 必须由 BFF 生成并绑 cookie**。`GetOAuthURLRequest.state` 字段不为空时,user-service 会原样存下来;BFF 应当:
   - 生成一个高熵随机 `state`(32 字节起)
   - 把 `state` 的 HMAC(用 BFF 自己的 cookie secret)写入 HttpOnly cookie
   - 浏览器跳去 OAuth 提供方时带着 state
   - 提供方跳回统一回调服务时,回调服务**必须验证**:query 里的 `state` 能匹配上 cookie 里的 HMAC
   - 不匹配直接 400,不调 `SocialLogin`

2. **`return_to` 必须用 user-service 的 allowlist 校验**,不允许 BFF 自行"信任前端传的 return_to"。配置:`cfg.OAuth.{provider}.AllowedRedirectURLs`。开发期可用 `AllowArbitraryRedirectURLs=true` 逃生,但生产**禁止**开启。

3. **PKCE 由 user-service 自动启用**(GitHub / Google / Apple;WeChat 不支持),BFF 无需参与。

4. **session_id 不能放进 return_to 的 URL query**。统一回调服务应该:
   - 生成一个一次性短 code(5 分钟过期)
   - 把 `session_id → short_code` 写入 Redis
   - 302 到 `return_to?code=<short_code>`
   - 业务方拿 short_code 调 user-service 换 session_id(`ExchangeSessionCode` RPC)

   URL 里的 session_id 会进 referer / 日志 / 浏览器历史,等于把 token 贴墙上。

5. **state 一次性消费**。user-service 用 Redis `GETDEL` 原子读删,重放会被拒。BFF 也应当在 cookie 里标记"已使用",防止双重提交。

下面是符合契约的 callback service 关键片段(替换下文 demo 里的对应部分):

```go
// 统一回调服务:验证 state cookie → 调 SocialLogin → 跳业务方
func oauthCallback(w http.ResponseWriter, r *http.Request) {
    code := r.URL.Query().Get("code")
    state := r.URL.Query().Get("state")
    if code == "" || state == "" {
        redirectOAuthError(w, r, "missing_params")
        return
    }

    // 1. 验证 state ↔ cookie 绑定(Login CSRF 防御,MUST)
    cookieState, err := r.Cookie("oauth_state")
    if err != nil || cookieState.Value == "" {
        redirectOAuthError(w, r, "missing_state_cookie")
        return
    }
    expectedState := hmacState(cookieState.Value, cookieSecret) // BFF 自己的 secret
    if !hmac.Equal([]byte(state), []byte(expectedState)) {
        redirectOAuthError(w, r, "state_cookie_mismatch")
        return
    }

    // 2. 调 user-service(state 已经在 GetOAuthURL 时绑过 PKCE verifier)
    resp, err := userClient.SocialLogin(r.Context(), &userv1.SocialLoginRequest{
        Provider: provider,
        Code:     code,
        State:    state,
    })
    if err != nil {
        redirectOAuthError(w, r, classifyOAuthError(err))
        return
    }

    // 3. 一次性短 code 模式:不把 session_id 放 URL,调 user-service 换短 code
    issueResp, err := userClient.IssueSessionCode(r.Context(), &userv1.IssueSessionCodeRequest{
        SessionId: resp.SessionId,
    })
    if err != nil {
        redirectOAuthError(w, r, "internal_error")
        return
    }

    // 4. 清掉 oauth_state cookie,重定向到 return_to
    http.SetCookie(w, &http.Cookie{
        Name: "oauth_state", MaxAge: -1, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
    })
    redirectURL := resp.ReturnTo + "?code=" + issueResp.GetCode()
    http.Redirect(w, r, redirectURL, http.StatusFound)
}

// hmacState returns HMAC-SHA256(secret, nonce) as a hex string. The BFF
// stores nonce in a cookie, sends HMAC as the OAuth state; on callback,
// recompute HMAC(cookie_nonce) and compare to query state.
func hmacState(nonce string, secret []byte) string {
    h := hmac.New(sha256.New, secret)
    h.Write([]byte(nonce))
    return hex.EncodeToString(h.Sum(nil))
}
```

**统一回调服务最小实现参考(Go,~70 行):**

```go
package main

import (
    "errors"
    "log"
    "net/http"
    "net/url"

    "github.com/servekit/go-common/xerr"
    userv1 "user-service/gen/user/v1"
    "user-service/pkg/xcodes"
)

var (
    client       userv1.UserServiceClient // 初始化 gRPC client
    errorPageURL = "https://auth.corp.com/oauth/error" // 用户可见错误页
)

// oauthCallback 处理所有 provider 的回调。OAuth 提供方注册的 redirect_uri
// 必须指向这个 handler 的公网地址(比如 https://auth.corp.com/oauth/callback)。
func oauthCallback(w http.ResponseWriter, r *http.Request) {
    code := r.URL.Query().Get("code")
    state := r.URL.Query().Get("state")
    if code == "" || state == "" {
        redirectOAuthError(w, r, "missing_params")
        return
    }

    // 1. Verify state ↔ cookie binding (Login CSRF defense, see §Security contract)
    cookieState, err := r.Cookie("oauth_state")
    if err != nil || cookieState.Value == "" {
        redirectOAuthError(w, r, "missing_state_cookie")
        return
    }
    expectedState := hmacState(cookieState.Value, cookieSecret)
    if !hmac.Equal([]byte(state), []byte(expectedState)) {
        redirectOAuthError(w, r, "state_cookie_mismatch")
        return
    }

    // 通过 provider path 区分,比如 /oauth/callback/github、/oauth/callback/google
    // 或者从 state 里解码出 provider
    provider := userv1.IdentityProvider_IDENTITY_PROVIDER_GITHUB // 例

    // 2. SocialLogin (state has PKCE verifier bound from GetOAuthURL time)
    resp, err := client.SocialLogin(r.Context(), &userv1.SocialLoginRequest{
        Provider: provider,
        Code:     code,
        State:    state,
    })
    if err != nil {
        redirectOAuthError(w, r, classifyOAuthError(err))
        return
    }

    // 3. Mint a one-time short code for the business side. NEVER put
    //    session_id in URL query — it leaks via Referer, browser history,
    //    CDN logs, browser extensions, screenshots.
    issueResp, err := client.IssueSessionCode(r.Context(), &userv1.IssueSessionCodeRequest{
        SessionId: resp.SessionId,
    })
    if err != nil {
        redirectOAuthError(w, r, "internal_error")
        return
    }

    // 4. Clear oauth_state cookie, redirect to return_to with short code.
    http.SetCookie(w, &http.Cookie{
        Name: "oauth_state", MaxAge: -1, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
    })
    redirectURL := resp.ReturnTo + "?code=" + issueResp.GetCode()
    http.Redirect(w, r, redirectURL, http.StatusFound)
}

// classifyOAuthError 把 user-service 返回的错误码翻译成稳定字符串。
// 这些字符串会出现在 URL query 里给错误页展示,**绝不要透传 err.Error()** ——
// 那会泄露内部细节(栈、provider 响应、用户标识)给攻击者。
//
// 粒度问题:xerr.Error 的 message 是 unexported,errors.Is() 按 reason 比较,
// 所以同一 reason 下的不同 message(如 "missing state" vs "state mismatch")
// 没法用 errors.Is 区分。如果业务方需要更细的错误码(state_expired vs
// state_provider_mismatch),需要让 user-service 给它们分配独立的 xcode。
// 详见 follow-ups 文档的"待澄清 / 任务 11"。
func classifyOAuthError(err error) string {
    var xe *xerr.Error
    if !errors.As(err, &xe) {
        return "internal_error"
    }
    switch xe.Code().Reason() {
    case xcodes.ErrUserDisabled.New().Code().Reason():
        return "user_disabled"
    case xcodes.ErrOAuthFailed.New().Code().Reason():
        return "oauth_failed" // ExchangeCode 失败 — code 过期 / 网络问题 / provider 拒绝
    case xcodes.ErrBadRequest.New().Code().Reason():
        // state 过期 / state mismatch / provider 不支持 等,统一成 bad_request。
        // 想细分需要 user-service 给每个加独立 xcode(见函数注释)。
        return "bad_request"
    default:
        return "internal_error"
    }
}

// redirectOAuthError 302 跳到错误页,带稳定错误 code + 原始 return_to(可选)。
// 用 query param 而不是 path,错误页用模板读出来展示。
func redirectOAuthError(w http.ResponseWriter, r *http.Request, code string) {
    q := url.Values{}
    q.Set("code", code)
    if rt := r.URL.Query().Get("return_to"); rt != "" {
        q.Set("return_to", rt) // 让错误页能"回登录"按钮
    }
    http.Redirect(w, r, errorPageURL+"?"+q.Encode(), http.StatusFound)
}

func main() {
    http.HandleFunc("/oauth/callback", oauthCallback)
    log.Fatal(http.ListenAndServe(":8080", nil))
}
```

**错误 code 字典(稳定,前端可硬编码):**

| code | 触发条件 | 用户应做的事 |
|------|----------|-------------|
| `missing_params` | URL 缺 code 或 state | 提示链接损坏,重新发起登录 |
| `bad_request` | state 不存在 / 已过期 / provider 不匹配 / provider 未配置 | 提示重试登录(state 过期常见) |
| `oauth_failed` | ExchangeCode 失败(code 无效 / 网络问题 / provider 拒绝) | 提示稍后重试 |
| `user_disabled` | 找到用户但状态是 DISABLED | 提示账号被禁用,联系客服 |
| `internal_error` | user-service 内部错误 | 提示稍后重试,告警 oncall |

`SocialLogin` 失败时**不要直接把 `err.Error()` 透传给用户** —— 那会泄露内部细节。错误页应该是个友好提示 + "重试登录"按钮。

生产环境这个服务还应该:

- **限流**:回调端点本身也要限频,防 provider 被滥用
- **多 provider 路由**:`/oauth/callback/{provider}` 或 state 里编码 provider

#### 业务方收到 short code 后的处理(MUST)

业务方的 `return_to` handler 必须做三件事:

1. 拿到 URL query 里的 `code`
2. 调 user-service 的 `ExchangeSessionCode(code)` 换 `session_id` + `user_id`
3. 用 `session_id` set **自己域名**的 HttpOnly + Secure + SameSite=Lax cookie

代码示例:

```go
func handleAuthDone(w http.ResponseWriter, r *http.Request) {
    code := r.URL.Query().Get("code")
    if code == "" {
        http.Error(w, "missing code", http.StatusBadRequest)
        return
    }
    resp, err := userClient.ExchangeSessionCode(r.Context(), &userv1.ExchangeSessionCodeRequest{
        Code: code,
    })
    if err != nil {
        http.Error(w, "invalid or expired code", http.StatusUnauthorized)
        return
    }
    http.SetCookie(w, &http.Cookie{
        Name:     "usid",
        Value:    resp.SessionId,
        Path:     "/",
        HttpOnly: true,
        Secure:   true,
        SameSite: http.SameSiteLaxMode,
        MaxAge:   7 * 24 * 3600, // match session TTL
    })
    // Redirect to the actual app UI
    http.Redirect(w, r, "/", http.StatusFound)
}
```

**注意**:
- `code` 是一次性的,刷新页面或重复访问会失败 — 业务方应在拿到 code 后立刻 set cookie 并跳走
- 如果业务方在多个顶级域(`a.com` / `b.com`),每个域都要走自己的 `return_to` handler,user-service 不参与跨域 cookie 共享

`is_new=true` 表示刚创建的账号,**没有密码**(social identity 没有 credentials)。如果业务需要让 social 用户也能用密码登录,引导他们走 `ChangePassword` 设置一个。

### 绑定 / 解绑 identity

> **`user_id` 字段约定**:`BindIdentity` / `BindOAuthIdentity` / `UnbindIdentity` / `ResetPassword` / `DisableUser` 等 RPC 的请求里都**显式带 `user_id`**(在 proto 中标注 `[(buf.validate.field).int64.gt = 0]`)。
>
> user-service 当前**不内置认证拦截器**(`grpcx.GetUserIDFromCtx` 还没接入),所以 `user_id` 必须由**调用方(BFF / 网关)从 session token 解出后注入**,客户端传的 `user_id` 不可信。后续接入认证拦截器后,服务端会用 ctx 里的 user_id 覆盖请求字段,彻底消除伪造风险。
>
> 服务端已经做的强制校验:
> - `BindIdentity`:user_id 必须存在(`GetUserByID` 预检)
> - `BindOAuthIdentity`:user_id 必须存在 + 校验 OAuth identity 没被其他用户占用
> - `UnbindIdentity`:**`identity.user_id == req.user_id`** 才允许删(防越权)
> - 其他几个 RPC 也都校验 user_id 在 DB 中存在

两类绑定走两个不同 RPC,因为前置条件差异大:

**EMAIL / PHONE 绑定**(`BindIdentity`):用户已经在登录态,需要把第二个邮箱或手机挂到当前账号。

```
1. SendVerificationCode(purpose=BIND, target=email/phone)
2. BindIdentity(user_id, provider=EMAIL/PHONE, email/phone, code, [password])
   → 检查 target 是否已被其他用户占用(ErrIdentityExists)
   → 创建 identity,password 字段可选(传了就成为密码登录凭证)
   → 顺手回填 user.Email / Phone / RegionCode(老账号没有时)
```

**OAuth 绑定**(`BindOAuthIdentity`,支持 GitHub / Google / WeChat web / Apple):用户已经在登录态,要把 OAuth identity 挂到当前账号。**不要走 `SocialLogin`** —— 那是登录用的,在 identity 不存在时会新建账号,而不是绑到现有 user_id。

跟登录走**同一套统一回调架构**(上面的流程图),区别是:回调服务拿到 `code+state` 后,如果调用方有 session(user_id 已知),应该调 `BindOAuthIdentity` 而不是 `SocialLogin`。常见做法是回调服务按"是否已登录"路由:

```go
// 统一回调服务内部
if sessionID, _ := getCookie(r, "usid"); sessionID != "" {
    // 已登录 → 走绑定
    userID := userIDFromSession(sessionID)
    resp, err := client.BindOAuthIdentity(ctx, &userv1.BindOAuthIdentityRequest{
        UserId:   userID,
        Provider: provider,
        Code:     code,
        State:    state,
    })
    // 绑定完成后跳回用户原来的页面(比如设置页),return_to 由前端在 GetOAuthURL 时传
} else {
    // 未登录 → 走登录
    resp, err := client.SocialLogin(ctx, &userv1.SocialLoginRequest{
        Provider: provider,
        Code:     code,
        State:    state,
    })
    // 走上面的"302 + session cookie"流程
}
```

`BindOAuthIdentity` 的语义:

```
1. GetOAuthURL(provider=WECHAT, return_to=<用户原所在页面>, [state])
   → 服务端把 state 存到 Redis(10min TTL),返回授权 URL
2. 用户在 OAuth 提供方页面上同意授权 → 浏览器跳到统一回调服务?code=...&state=...
3. 统一回调服务检测到已登录 cookie → 调:
   BindOAuthIdentity(user_id, provider=WECHAT, code, state)
   → 校验 state(必须在 Redis 里且 provider 匹配,一次性消费)
   → ExchangeCode 拿到 OAuth UID
   → UID 已绑给其他用户 → ErrIdentityExists
   → UID 已绑给当前 user_id → 幂等返回现有 identity
   → 否则创建 identity,user.Email / AvatarURL / Nickname 为空时顺手回填
   → 不创建新 session(调用者已登录)
```

**解绑**(`UnbindIdentity`):请求必须带 `user_id`,服务端校验 `identity.user_id == req.user_id` 后才允许删除;`code` 是发给该 identity 自己 target 的验证码(EMAIL 用 VERIFY_EMAIL,PHONE 用 VERIFY_PHONE)。最后一条带密码的 identity 不允许解绑(避免锁死)。

### 微信小程序登录

两条路径,根据是否需要手机号选择:

**路径 A:静默登录(只要 openid)**

```
MiniProgramLogin(code)            // wx.login() 拿到的 code
  → 返回 LoginResponse,用户以 openid 标识
```

**路径 B:登录 + 获取手机号**

```
1. MiniProgramLogin(login_code)   // 第一次调用,创建/找到 openid 用户
2. MiniProgramPhoneLogin(login_code, phone_code)
   - login_code: 还是 wx.login() 的 code(同一个登录态)
   - phone_code: <button open-type="getPhoneNumber"> 拿到的 code
   → 已有手机号用户:把 miniprogram identity 链到该账号
   → 新用户:建用户 + 同时挂 phone 和 miniprogram 两个 identity
```

`MiniProgramPhoneLogin` 也可以单独调用,不先调 `MiniProgramLogin`。两种调用顺序都支持。

### 密码重置(忘记密码)

`SendVerificationCode(purpose=PASSWORD_RESET)` → `ResetPassword`

`ResetPassword` 校验 `purpose=PASSWORD_RESET` 的验证码,找到对应 email/phone 的 identity,更新其凭证哈希,然后撤销该用户所有现有 session(强制用新密码重新登录)。

### 管理员建号 + 激活

`CreateUser`(管理员)→ 用户拿到初始密码 → `ChangePassword`(激活)

```
1. CreateUser(user_type, username, email/phone, password, ...)
   → 用户以 PENDING_REVIEW 状态创建,密码已 bcrypt 哈希
   → 此时账号无法登录(Login 会拒)
2. 把初始密码告诉用户(邮件/短信/口头)
3. 用户用 ChangePassword(user_id, old_password=初始密码, new_password)
   → 校验旧密码 → 写入新密码 → 状态翻转为 ACTIVE
   → 撤销该用户所有现有 session(包括本次调用所在的 session)
   → 账号可用,用户需用新密码重新登录
```

注意 `ChangePassword` 的副作用:
- **只有 PENDING_REVIEW 状态下成功的 ChangePassword 才会激活**;ACTIVE 状态用户调用只是普通改密
- **任何状态下都会撤销该用户所有现有 session**,跟 `ResetPassword` 行为一致 —— 防"账号被接管后改密,攻击者 session 仍然有效"窗口。BFF 调完 `ChangePassword` 后必须再调 `Login` 拿新 session,不能直接续用现有 session

### Session 管理

| 操作 | RPC |
|---|---|
| 列出当前用户所有活跃 session(管理设备) | `ListSessions(user_id)` |
| 用 session_id 反查 user_id + 元数据(BFF 认证闭环) | `GetSession(session_id)` |
| 注销当前 session | `Logout(session_id)` |
| 注销某个 session(管理员/本人) | `RevokeSession(session_id)` |
| 注销用户全部 session(改密/封号后) | `RevokeAllSessions(user_id)` |
| 续期 session(滑动窗口) | `RefreshSession(session_id)` |

#### 业务方认证闭环(BFF)

user-service 没有内置认证拦截器,所有需要 user_id 的 RPC 都依赖调用方(BFF/网关)从 session token 解出 user_id 后注入。BFF 拿到 cookie 里的 session_id 后,用 `GetSession` 反查 user_id:

```
浏览器              BFF (a.com)                    user-service
  │                    │                                │
  │ ① 请求 a.com 带     │                                │
  │   Cookie: usid=...  │                                │
  │ ─────────────────>  │                                │
  │                    │ ② GetSession(session_id)        │
  │                    │ ──────────────────────────────> │
  │                    │                                │
  │                    │    Redis 校验 + sliding refresh │
  │                    │ <── { user_id, expires_at, ... }│
  │                    │                                │
  │                    │ ③ 后续 RPC 用 user_id 调用:    │
  │                    │    GetProfile(user_id)          │
  │                    │    UpdateProfile(user_id, ...)  │
  │                    │    BindIdentity(user_id, ...)   │
  │                    │ ──────────────────────────────> │
  │                    │                                │
  │ ④ 业务响应          │                                │
  │ <─────────────────  │                                │
```

要点:
- `GetSession` 是**Redis-only**(不查 DB),延迟低,适合每请求都调
- 同其他 session 读,它**滑动续期 TTL**(等同于 `RefreshSession` 的副作用)
- 失败响应(过期 / 不存在的 session_id)返回 `ErrSessionInvalid`,BFF 应清除 cookie 并重定向到登录
- 返回的字段:`user_id`(主)、`expires_at`(从 Redis TTL 推算)、`created_at`、`ip`、`user_agent`、`os`、`browser`、`login_method`。**不返回 `last_active_at`**(那字段在 DB,避免每请求都查)

Session 双层存储:PostgreSQL(`user_sessions` 表,审计 + 重启后可恢复)+ Redis(快速校验 + TTL)。

**一致性模型:** Redis 写入在 DB 事务闭包内执行,但不参与 DB 事务——Redis 操作不可回滚。所以"两者同步更新"是按"全有或全无"的语义保证:

- **创建 session(Login/Register)**:DB 写 → Redis 写,任何一步失败整个事务回滚。极端情况下 Redis 写成功 + DB commit 失败时,Redis 会留下指向不存在用户的孤儿 session,下次校验时 `GetUserByID` 失败而拒绝,且 key 在 TTL 后自动过期。
- **撤销 session(Revoke/Disable)**:DB 改状态 → Redis 删 key。极端情况下 Redis 删成功 + DB commit 失败时,DB 仍显示 active,但 Redis 已无 key——session 无法再用,属于"过度撤销"但安全。

跨资源的真正原子性需要 outbox / saga,目前未引入。如果对审计准确性有强要求,需要做对账任务比对 DB 与 Redis。

### RBAC 配置

RBAC 是 5 个实体配合:**Permission**(只读目录)、**PermissionGroup**(权限包)、**Role**(角色)、**Group**(用户组)、**User**(用户)。

**典型配置流程:**

```
1. ListPermissions / ListPermissionGroups
   → 浏览可用的 permission / permission group

2. CreateRole(name, permission_ids, permission_group_ids)
   → 创建角色,绑定一组权限

3a. 直接给用户授权:
    AssignRole(user_id, role_id)

3b. 或者按组授权(组成员继承):
    CreateGroup(name) → AddGroupMember(group_id, user_id, role) → AddGroupRole(group_id, role_id)
```

**权限检查**(由调用方/middleware 做):调用 `rbac.GetUserPermissions(ctx, userID)` 拿到 `[]PermissionEntry`(resource+action 列表),根据业务策略判定。结果会缓存在 Redis,任何 `AddGroupMember` / `AssignRole` / `UpdateRole` 等变更都会主动失效相关用户的缓存。

---

## 作为 Go Module 使用

适合父项目想要"用户能力"但不想引入网络跳转的场景。

```go
import (
    "context"
    usersvc "user-service/internal/service"
    "user-service/pkg/config"
    "user-service/pkg/option"
    uspb "user-service/gen/user/v1"
)

// 方式 1:从 config.yaml 构造(默认自建 DB + Redis)
cfg, err := config.Load("config.yaml")
svc, err := usersvc.New(cfg)
if err != nil { return err }
defer svc.Stop()

// 启动后台任务(session reaper 等)
if err := svc.Start(); err != nil { return err }

// 直接调方法,不走网络
resp, err := svc.Register(ctx, &uspb.RegisterRequest{ ... })
```

```go
// 方式 2:注入已有的 DB / Redis 连接(父项目接管生命周期)
svc, err := usersvc.New(cfg,
    option.WithDB(parentDB),
    option.WithRedis(parentRedis),
    // option.WithGIDService(...) / WithMessageService(...) 同理
)
// 注入的资源 svc.Stop() 不会关闭,由父项目自己管理
```

**注意:**

- 所有 RPC 方法签名都接收/返回 proto 类型(`*pb.XxxRequest` → `*pb.XxxResponse`)
- 顶层 `Service` 是 facade,每个方法一行委托到对应 subpackage(`auth`/`user`/`session`/`social`/`rbac`)
- 调用 `Start()` 后台任务前,父项目应确保 DB / Redis 已就绪
- `Stop()` 是 LIFO 并发关闭;注入的资源不会被关

---

## 作为 Go 模块使用(in-process 模式)

user-service 既可以独立部署为 gRPC 服务(配合统一回调 HTTP 服务,见上文),也可以**作为 Go 模块嵌入到你的应用进程里**。模块模式下整个 OAuth 流从 `GetOAuthURL` 到 `SocialLogin` 都在同一个进程,不需要统一回调服务,也不需要 `return_to` 路由。

### 模块模式 vs 服务模式

| 维度 | gRPC 服务模式(统一回调架构) | 模块模式(in-process) |
|------|------------------------------|----------------------|
| OAuth 提供方注册的回调 URL | 一个独立的"统一回调服务"地址 | **嵌入应用自己的 HTTP handler** |
| 调 `SocialLogin` 的人 | 统一回调服务 | **嵌入应用直接调** |
| `return_to` 的作用 | 告诉回调服务"业务方在哪" | **用不到,留空** |
| `AllowedRedirectURLs` | 业务方白名单,必配 | **不需要配** |
| `AllowArbitraryRedirectURLs` | 逃生口 | **不需要配** |
| `state` ↔ cookie 绑定 | BFF 必须做 | 嵌入应用自己用任何 session 机制 |
| `IssueSessionCode` / `ExchangeSessionCode` RPC | callback 服务用 | **不用** — 嵌入应用直接拿 `session_id` |

### 模块模式接入示例

```go
package main

import (
    "context"
    "net/http"

    "user-service/internal/identity/github"
    "user-service/internal/service/social"
    "user-service/pkg/config"
)

func main() {
    // 1. 构造 social.Service,RedirectURL 指向嵌入应用自己的路由
    githubProv := github.New("client-id", "client-secret",
        "https://myapp.com/auth/github/callback") // ← 嵌入应用的回调 endpoint

    cfg := &config.OAuthConfig{
        GitHub: &config.OAuthGitHubConfig{
            ClientID:     "client-id",
            ClientSecret: "client-secret",
            RedirectURL:  "https://myapp.com/auth/github/callback",
            // AllowedRedirectURLs 不配 — 模块模式用不到
            // AllowArbitraryRedirectURLs 不配 — 默认 false
        },
    }

    socialSvc, warnings, err := social.New(db, sessionMgr,
        map[pb.IdentityProvider]identity.SocialProvider{
            pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB: githubProv,
        }, gid, rdb, cfg.OAuth)
    if err != nil {
        panic(err)
    }
    _ = warnings // 模块模式下不打日志(无 logger 注入);advisory only,实际由 cmd/server 路径消费

    // 2. 用户点"用 GitHub 登录" → 跳 OAuth 提供方
    http.HandleFunc("/login/github", func(w http.ResponseWriter, r *http.Request) {
        // 模块模式关键:return_to 留空
        resp, err := socialSvc.GetOAuthURL(r.Context(), &pb.GetOAuthURLRequest{
            Provider: pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB,
            ReturnTo: "", // ← 模块模式标志
        })
        if err != nil {
            http.Error(w, err.Error(), http.StatusInternalServerError)
            return
        }
        http.Redirect(w, r, resp.Url, http.StatusFound)
    })

    // 3. GitHub 回调到嵌入应用的路由(路径必须和 cfg.RedirectURL 一致)
    http.HandleFunc("/auth/github/callback", func(w http.ResponseWriter, r *http.Request) {
        code := r.URL.Query().Get("code")
        state := r.URL.Query().Get("state")
        if code == "" || state == "" {
            http.Error(w, "missing params", http.StatusBadRequest)
            return
        }

        resp, err := socialSvc.SocialLogin(r.Context(), &pb.SocialLoginRequest{
            Provider: pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB,
            Code:     code,
            State:    state,
        })
        if err != nil {
            http.Error(w, err.Error(), http.StatusUnauthorized)
            return
        }

        // 4. 嵌入应用自己 set cookie、自己跳自己的页面
        http.SetCookie(w, &http.Cookie{
            Name:     "usid",
            Value:    resp.SessionId,
            Path:     "/",
            HttpOnly: true,
            Secure:   true,
            SameSite: http.SameSiteLaxMode,
            MaxAge:   7 * 24 * 3600,
        })
        http.Redirect(w, r, "/dashboard", http.StatusFound) // ← 自己跳,不靠 return_to
    })

    http.ListenAndServe(":8080", nil)
}
```

### 模块模式下的 state ↔ cookie 绑定(仍然 MUST)

即便在模块模式,**Login CSRF 风险依然存在**(攻击者把自己的 OAuth `code` 喂进受害者的 state)。嵌入应用必须自己绑:

```go
// 启动登录时
nonce := generateRandomString(32)
setCookie(w, "oauth_state", nonce) // 嵌入应用自己用 securecookie
state := hmacSHA256(nonce, appSecret) // 把 nonce 转 state

socialSvc.GetOAuthURL(ctx, &pb.GetOAuthURLRequest{
    Provider: pb.IdentityProvider_IDENTITY_PROVIDER_GITHUB,
    ReturnTo: "",
    State:    state, // ← 嵌入应用自己传 state(走 caller-supplied state 路径)
})

// 回调时验证
cookieNonce := readCookie(r, "oauth_state")
expectedState := hmacSHA256(cookieNonce, appSecret)
if !hmac.Equal([]byte(r.URL.Query().Get("state")), []byte(expectedState)) {
    http.Error(w, "state mismatch", http.StatusBadRequest)
    return
}
// 再调 SocialLogin
```

或者更简单:直接把 nonce 作为 state 传,回调时比 cookie 就行(只要 nonce 足够随机,HMAC 不是必须)。

### 多实例部署的注意

模块模式下,state 存在 Redis 里(`oauth:state:<state>`),所以**多实例部署没问题** — 任何实例都能消费任何实例发的 state。session 也走 Redis。唯一要在嵌入应用层处理的是:cookie 的 `Domain` 要一致,或者用 sticky session。

### 什么时候用模块模式

- 嵌入应用已经有自己的 HTTP 服务(不想再起 gRPC + 回调服务)
- 单一业务方(不需要"多业务共用一套 OAuth App")
- 想要更简单的部署拓扑(一个进程搞定)
- 内部工具 / 中后台 / 单体应用

什么时候**不要**用模块模式:

- 多业务方共用 OAuth(必须走统一回调架构)
- 跨顶级域 cookie 共享(模块模式不解决跨域)
- 想要 user-service 独立升级 / 扩缩容(模块模式把 user-service 编进嵌入应用二进制)

---

## 错误码

所有错误都是 `xerr.Error`(go-common/xerr),带 reason / category / httpCode / message。

- **业务错误**:定义在 `pkg/xcodes/`(如 `ErrIdentityExists`、`ErrPasswordWrong`、`ErrUserDisabled`、`ErrTooManyRequests`)
- **通用错误**:用 `xcodes.ErrBadRequest`、`xcodes.ErrInternal`、`xcodes.ErrNotFound`
- **创建**:`xcodes.ErrXxx.New()` 或 `.New("override message")`
- **包装底层错误**:`xcodes.ErrXxx.Wrap(err)` / `.Wrapf(err, "ctx: %d", id)`
- **比较**:`errors.Is(err, xcodes.ErrXxx)`(xerr 已实现 `Is()` + `Unwrap()`)

---

## 未实现的接口

所有 proto 中定义的 RPC 均已实现。

---
