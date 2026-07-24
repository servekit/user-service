# user-service 遗留 TODO 清单

> **背景**: 2026-07-02 一次大规模 code review 后,修了 9 个原始问题 + 实现了 OAuth 绑定 + 改成方案 B 统一回调架构。本文记录 review 中提到但**当次未处理**的事项,供后续 session 接续。
>
> 对话归档:每条标注用户的明确指令(`做` / `不做` / `评估` / `记下`)。

---

## 待办(用户已确认要做)

### 1. 加 `GetSession` RPC(认证闭环)

**问题**:业务方拿到 `session_id` 后,没有任何 user-service RPC 能反查 `user_id`。所有需要 `user_id` 的 RPC(`BindIdentity` / `BindOAuthIdentity` / `UnbindIdentity` / `ResetPassword` / `DisableUser` / `GetProfile` …)BFF 都没法正确填充。

**方案**:新增只读 RPC。

```proto
message GetSessionRequest {
  string session_id = 1 [(buf.validate.field).string = {min_len: 1, max_len: 128}];
}

message GetSessionResponse {
  int64 user_id = 1;
  google.protobuf.Timestamp expires_at = 2;
  google.protobuf.Timestamp last_active_at = 3;
  string ip = 4;
  string user_agent = 5;
  string os = 6;
  string browser = 7;
}

rpc GetSession(GetSessionRequest) returns (GetSessionResponse) {
  option (google.api.http) = {get: "/v1/sessions/{session_id}"};
}
```

**实现**:`internal/service/session/session.go` 加 `GetSession` 方法,内部调 `sessionMgr.Get`(只读 Redis,不写 DB)。

**README 配套**:补一段"业务方认证闭环"流程图(BFF 收到带 cookie 的请求 → 调 `GetSession` 拿 user_id → 注入业务 RPC)。

---

### 3. README 写清楚"统一回调 HTTP 服务"由调用方/网关搭建

**问题**:当前 README 给了 50 行 demo,但没明说"这是 user-service 之外、由网关团队负责的服务"。

**方案**:在 `README.md §"OAuth 重定向登录(统一回调架构)"` 顶部加一段醒目部署说明:

- user-service 是 gRPC-only,**不包含** HTTP 回调服务
- 该 HTTP 服务由**网关团队**负责搭建(或独立部署)
- 配置项 `cfg.OAuth.{provider}.RedirectURL` 必须指向该服务的公网地址
- demo 代码可参考,但不能直接用(生产需要加错误页 / 限流 / token 安全)

---

### 4. 给新 RPC 补单测

**问题**:这次新加/改的 RPC 几乎都没单测:

- `ResetPassword`(user.go)
- `BindIdentity`(user.go)
- `BindOAuthIdentity`(social.go)
- `UnbindIdentity`(user.go,含 ownership 校验路径)
- `ListUserRoles`(rbac/role.go,含 group-derived 路径)
- `ListGroupRoles`(rbac/role.go)
- `DisableUser`(user.go,改 + revoke session 路径)

**方案**:逐个补。模式参考已有的 `internal/service/auth/auth_test.go`(unit)和 `internal/service/service_test.go`(用 `redisx.NewTestClient` + `dbx.SetupTestDB` 跑集成)。每个 RPC 至少覆盖 happy path + 1-2 个 error path。

---

### 5. `ListSessions` 改成 Redis pipeline / MGet(性能)

**问题**:`internal/service/session/session.go:54`:

```go
for _, sid := range sessionIDs {
    data, err := s.sessionMgr.Get(ctx, sid)  // 每个 session 一次 round-trip
    ...
}
```

用户 5 个 session 就是 5 次 Redis 调用。

**方案**:在 `Manager` 上加一个 `GetMulti(ctx, sessionIDs) → map[string]*Data` 方法,内部用 `client.MGet` 拿 raw payload,然后批量 `json.Unmarshal`。注意 `MGet` 不会顺带 refresh TTL,所以要么:

- 接受这个 trade-off(列表场景不续期是合理的)
- 或在列表里需要 refresh 的项单独再走一次 `luaGetAndRefresh`

倾向前者,跟现在的"每次 Get 都续期"语义有差异,但符合"列表 ≠ 校验"的语义。改之前确认下产品上是否要求 ListSessions 也滑动续期。

---

### 9. `OAuth.RedirectURL` 配置错误难排查

**问题**:`internal/service/social/social.go` 的 `providerRedirectURL` 直接返回 `cfg.OAuth.{provider}.RedirectURL`,如果运维填错(末尾多斜杠 / http vs https / 拼写),`GetOAuthURL` 拼出来的授权 URL 会让 OAuth 提供方报"redirect_uri mismatch",但错误信息很难看。

**方案评估**(需要和用户确认选哪个):

- **A. 启动时校验**:在 `New()` 里加 `validateOAuthConfig(cfg)`,检查每个 provider 的 `RedirectURL` 是合法 URL,非空。不做内容校验(因为没法知道 provider 那边注册了什么)。
- **B. 加 trim+normalize**:`providerRedirectURL` 里 `strings.TrimSuffix(url, "/")`,消除常见的末尾斜杠差异。
- **C. 文档强调**:在 `config.example.yaml` 里加注释,告知"必须精确匹配 OAuth provider 后台注册的值,包括 scheme 和末尾斜杠"。

建议 A+C(B 太魔法,可能掩盖真实配置错误)。

---

### 11. OAuth 失败页

**问题**:README 的 50 行 demo 里:

```go
if err != nil {
    http.Error(w, err.Error(), http.StatusUnauthorized)
    return
}
```

太裸,用户看到的就是 401 + 错误字符串。

**方案**:

- 把 README demo 改成 302 跳到一个错误页 URL(比如 `cfg.ErrorPageURL`),把错误 code 通过 query param 传
- 错误 code 用稳定字符串(比如 `oauth_failed` / `state_expired` / `provider_mismatch`),不直接透传 err.Error()(避免泄露内部信息)
- 真正的 callback 服务在网关侧实现时,网关团队按这个模式做

---

### 12. email/phone 验证状态(`user_identities.verified`)

**问题**:`Register` / `BindIdentity` 都直接写 `Verified=true`,字段形同虚设。没有"邮箱验证链接 → 点击才置 true"的标准流程。

**方案评估**(需要和用户确认):

- **A. 完整邮箱验证流程**:SendVerificationCode 加一个 `VERIFY_EMAIL` 模板,生成 token,发邮件带链接,链接里有 token,用户点链接调一个新的 `ConfirmEmail` RPC 把 identity 的 verified 置 true。改动大。
- **B. 简化:把验证码 = 验证**:既然注册时已经发了验证码并且校验通过,逻辑上 email 已经被验证了。直接把现有流程的语义明确化(注释里说明"通过 captcha 校验即视为 verified"),`Verified=true` 保持。改动小。
- **C. 把字段删了**:既然不用,删了避免误导。但后续如果要加 OAuth 注册后要求用户补验邮箱,这个字段又得加回来。

建议 B(成本低,语义清晰)。

---

### 13. `ChangePassword` 修正:撤销其他 session

**问题**:`ResetPassword` 撤销了所有 session,但 `ChangePassword`(用户自己改密)不撤销。语义不一致:用户主动改密后,旧 session 还能用,这是潜在风险(账号被接管后改密,攻击者和被攻击者都能登)。

**方案**:在 `internal/service/user/user.go` 的 `ChangePassword` 末尾加:

```go
if s.revoker != nil {
    if err := s.revoker.RevokeAllByUserID(ctx, userID); err != nil {
        return nil, err
    }
}
```

跟 `ResetPassword` 行为对齐。**注意**:这会让用户改密后被强制重新登录,确认产品上能接受。

---

## 已记下(后续做,不在本次范围)

### 7. 限流维度扩展(IP / 设备指纹)

**问题**:当前 `loginLimiter` / `codeLimiter` 都按 `target`(邮箱/手机)限流。攻击者换 target 就能绕过。

**TODO**:加 IP / 设备指纹维度。可能要:

- 从 gRPC metadata 提取 `X-Forwarded-For` 或 `X-Real-IP`(由网关注入)
- 加一个 `ratelimit.Config` 的 IP-based rule
- 设备指纹需要前端配合传 device fingerprint(参考 fingerprintjs)

**记下,后续做**。

---

## 不做(用户明确否决)

### 2. user-service 认证拦截器

**用户回复**:内网微服务,安全由网络层保证,其他服务调用是可信的。

**结论**:**不做**。`user_id` 字段继续由调用方注入,服务端不强制。`UnbindIdentity` 的 ownership 校验作为兜底保留。

### 8. 密码强度(大小写/数字/符号)

**用户回复**:先放一放。

**结论**:**不做**。proto 保持 `min_len=8 max_len=128`。

---

## 待澄清(用户没看明白,需要解释)

### 10. 跨顶级域 cookie

**用户回复**:没明白。

**需要解释的**:

如果业务方在不同顶级域(比如 `a.com` 和 `b.com`,不是 `a.corp.com` 和 `b.corp.com`),统一回调服务 set 的 cookie(`Domain=.corp.com`)**不能跨顶级域共享**——浏览器拒绝。所以业务方在 `a.com` 拿不到 `auth.corp.com` set 的 cookie。

**解法**(几选一,跟用户确认):

- **A. 所有业务方共用一个顶级域**(`*.corp.com`),cookie 走顶级域。最简单,要求公司域名统一。
- **B. URL 参数传递 session_id**:`auth.corp.com` 把 session_id 放在 302 的 Location URL 里(`a.com/done?token=xxx`),业务方收到后自己 set 自己域名的 cookie。**风险**:token 进 referer / 日志 / 浏览器历史,生产建议改成"一次性短 code → 业务方拿 code 调 user-service 换 token"。
- **C. SSO 模式**:统一回调服务签发一个 JWT,放在 URL 参数里;业务方自己验 JWT。要求 JWT 公钥分发。

下次跟用户解释这三选一,让他选。

---

## 已完成(参考,本次 session 已 ship)

- `590e53f` feat(user): revoke all sessions on password change
- `717c312` docs(identity): clarify Verified=true semantics
- `2f2bf19` docs(readme): show OAuth callback error handling with stable codes
- `c1e4f67` feat(social): validate OAuth redirect_url at startup
- `ff13fb9` perf(session): fetch ListSessions via single MGet round trip
- `81d30da` docs(readme): highlight callback service deployment responsibility
- `49e5397` feat(session): add GetSession RPC for BFF auth loop
- `d10e9b9` fix(auth): resolve captcha from config so RPCs do not panic
- `0814cdf` build: align genconfig OutPath with Makefile output dir
- `c89a479` fix(user): store username as nullable to avoid unique collision
- `01bd453` fix(user): revoke sessions when disabling a user
- `0a6fb99` feat(user,rbac): implement ResetPassword, Bind/UnbindIdentity, List*Roles
- `46c8010` fix(social): enforce OAuth state and per-provider redirect allowlist
- `6b41771` fix(auth): fail rate limiters closed and cap SendVerificationCode
- `0793c61` fix(apple): verify Apple ID token signature, iss, aud, and exp
- `30bdf67` docs(session): correct cross-resource atomicity claim
- `c28bf0d` style: fix gofmt alignment and lint nits
- `0b675d2` feat(identity): implement OAuth bind and harden UnbindIdentity ownership
- `dd258b4` fix(user): pre-check user existence in BindIdentity
- `88b30c7` feat(social): adopt unified OAuth callback architecture (plan B)
- `99f4570` docs(readme): document user_id field contract for caller-injected RPCs

---

## 接续指南

下次开新 session 接续时:

1. 让 Claude 读这份文件:`cat docs/follow-ups-2026-07-02.md`
2. 按"待办"区块从上往下做;每条都标了具体位置 / 改动思路 / 需要决策的点
3. 需要用户决策的(标了"评估"或"建议"):先跟用户对齐再动
4. 完成一条就 git commit + 在本文最下面的"已完成"列表里追加
