# PVZCE 启动登录交接 v1 后端

本变更实现 `pvzce/site` 的后端接口，默认关闭，不代表已部署或游戏端到端可用。
游戏和启动器不在本变更范围内。当前游戏尚未消费/脱敏参数，仍应离线启动；不得为游戏添加能力声明。

字段依据：启动器草稿 [PVZCELauncher #5](https://github.com/pvzce/PVZCELauncher/pull/5)
（依赖 #4），提交 `e6c9b64b6c501a290ef838724f59eb50060db245` 的
[完整契约](https://github.com/pvzce/PVZCELauncher/blob/e6c9b64b6c501a290ef838724f59eb50060db245/docs/PVZCE_GAME_LOGIN.md)、
`GameLoginProtocol.java` 与 `GameLoginCoordinator.kt`。码/令牌是本服务生成的不透明随机值，不能使用 Cookie 假冒游戏 Bearer。

## 配置和迁移

沿用既有 Casdoor OIDC 网站登录，必须先启用 `AUTH_ENABLED=true` 并配置 PostgreSQL。
后端显式启用示例（不是生产配置变更）：

```dotenv
APP_PUBLIC_URL=https://site.example
GAME_LOGIN_ENABLED=true
GAME_LOGIN_VERSIONS=1.0.0
GAME_LOGIN_CODE_TTL=60s
GAME_LOGIN_SESSION_TTL=1h
GAME_LOGIN_ALLOW_LOCAL_HTTP=false
```

- `GAME_LOGIN_VERSIONS` 是运维明确允许的实际游戏版本列表，逗号分隔，不能为空。版本字符串最多 64 个 ASCII 字符，允许字母、数字、`.`、`_`、`+`、`-`。不要将启动器版本当成游戏版本；启动器读取实际 JAR 的 `PvzceVersions` 常量。
- 短码 TTL 范围 1–90 秒，默认 60 秒；游戏会话 TTL 范围 1 秒–1 小时，默认 1 小时。两者有效期均不会超过来源网站会话。
- origin 从 `APP_PUBLIC_URL` 规范化得到：小写主机、去掉默认端口、保留非默认端口；没有路径、userinfo、query、fragment。不会使用 Host 或转发头生成 `api_url`。
- 生产只允许 HTTPS。开发需同时使用非生产 `APP_ENV` 和 `GAME_LOGIN_ALLOW_LOCAL_HTTP=true`；游戏交接 HTTP 仅允许 `localhost`、`127.0.0.1`、`::1`、`10.0.2.2`，还必须通过现有 Auth origin 校验（目前网站 Auth 对 HTTP 仅允许 localhost/loopback，因此模拟器地址目前不能直接配置为网站 origin）。
- Compose 已透传上述变量。默认关闭时接口返回 503，不声明能力；显式启用但配置不完整会阻止进程启动。
- HTTPS 反向代理须保留公开 Host（包括非默认端口），后端端口只应暴露给受控代理；TLS 由既有部署终止。`X-Forwarded-Host`/`X-Forwarded-Proto` 不改变可信 origin。不存在根据客户端请求自动重定向到其他站点的行为。

新增前向迁移 `007_game_login.sql`，不修改历史迁移；启动时沿用检查摘要和事务锁的自动迁移，也可先执行 `server migrate`。
新增 `pvzce_game.login_codes`、`sessions`、`rate_limits` 及账号限制触发器。不改变网站会话令牌格式。
能力接口实时检查数据库可写、必要表权限和撤销触发器；检查失败返回 503。
部署数据库角色需要这些新表的读写权限、Auth 会话/用户的读取及行锁权限、站点账号读取/创建及行锁权限；通常由现有迁移角色创建并拥有。
只读副本不能提供此功能。

## 接口

所有响应（包括错误）`Cache-Control: no-store`，JSON envelope 与网站保持一致。
没有开放 CORS；Origin 若提供必须严格等于规范化公开 origin，拒绝 cross-site fetch。
请求 Host 必须与配置 origin 一致，忽略所有客户端转发头。所有接口拒绝 query（包括空 `?`），凭证只允许 JSON/Header。
JSON 请求仅接受 `application/json`，最多 2 KiB，拒绝未知字段、重复键、大小写别名、非对象和尾随数据。错误不回显输入或数据库错误。

### `GET /api/v1/launcher/capabilities`

公开只读；只有显式启用、网站认证已初始化且所需存储可用时返回：

```json
{"data":{"game_login_protocol":1}}
```

### `POST /api/v1/launcher/login-codes`

必须携带现有 `pvzce_session` Cookie、`X-CSRF-Token` 和准确的 `Origin`；不接受 Authorization 或请求自报账号 ID。
先经现有 Cookie/CSRF 认证，再在签发事务中重新检查来源会话和未受限账号。

```json
{"protocol":1,"game_id":"pvzce","game_version":"1.0.0"}
```

```json
{"data":{"protocol":1,"api_url":"https://site.example","login_code":"<43字符Base64url无填充短码>","expires_at":"2026-10-20T00:01:00Z"}}
```

32 随机字节（256 位熵）产生 43 字符规范 Base64url 值，满足启动器 32–128 字符检查。
只保存 SHA-256 摘要，并绑定来源网站会话摘要、用户、origin、协议、游戏、版本和过期时间。
不导出 Casdoor 密码、refresh token、客户端密钥或网站 Cookie。不得自动循环签发重试。

### `POST /api/v1/game/login-code/exchange`

不依赖网站 Cookie/CSRF，不接受 Authorization。只认请求体短码：

```json
{"protocol":1,"game_id":"pvzce","game_version":"1.0.0","login_code":"<短码>"}
```

```json
{"data":{"token_type":"Bearer","access_token":"<43字符不透明游戏令牌>","expires_at":"2026-10-20T01:00:00Z","scope":["game:session"],"user":{"id":42,"display_name":"玩家"}}}
```

事务锁定账号和来源会话，条件删除未过期码，再插入游戏会话。任一步失败整体回滚。
只有同码并发中的一个请求成功。兑换提交后即不可重放；即使响应丢失也不会重建会话，应从启动器取得新码。
错误协议/游戏、未允许版本、绑定版本/origin 不匹配、来源退出/过期/撤销/受限均不能兑换。
origin 绑定指配置中的 API origin，不信客户端自报目的站点；无浏览器 Origin 的原生游戏可兑换，但不能通过伪造 Host/转发头切换绑定。

游戏令牌也是独立 32 字节随机值，仅存摘要，无 refresh token，唯一 scope 为 `game:session`。
用户 ID 只来自服务端已验证的网站会话；display_name 优先站点昵称，其次 OIDC name/username，不返回 email/subject/网站角色。

### `GET /api/v1/game/session`；`POST /api/v1/game/logout`

只接受一个 `Authorization: Bearer <游戏令牌>`，不接受 Cookie 作为替代、重复 Authorization 或请求体。
GET 返回 `{"data":{"user":...,"expires_at":...,"scope":["game:session"]}}`，不再回传令牌。
POST 成功 204，撤销当前会话；再次使用返回 401。
每次验证游戏令牌有效期、origin/协议/游戏/允许版本/scope、来源会话有效期和账号状态。
游戏令牌不能用于网站 `/api/v1/account/me`、上传、审核或管理接口，网站认证始终只认网站 Cookie。
游戏须兑换后再验证会话才显示已登录，令牌只留内存，失败切离线；这部分待游戏实现。

## 撤销、限流与日志

网站退出/会话替换删除来源 session，外键 `ON DELETE CASCADE` 在同一事务中撤销其所有码和游戏会话；其他网站会话不受影响。
既有管理员流程把 `pvzce_site.accounts.status` 改为 `restricted`，触发器永久删除该账号的现存授权，恢复 active 不会复活旧码/令牌。
签发/兑换/校验按“账号 → 来源会话 → 游戏凭证”的顺序锁定，限制与退出必须等待在途事务，然后撤销结果。
来源会话过期即时拒绝，即使尚未物理清理。人工撤销应删除对应站点 session（账号全部退出则删除该用户所有 session）；不要通过重新延长过期记录来模拟新登录。
此实现检查站点本地状态；Casdoor 中直接禁用用户/全局 SSO 注销不会凭空推送到本站。运维需通过既有站点限制/会话删除同步撤销；若需要自动同步外部 IdP 的注销或封禁，应另外接入并验证可信回调，不能宣称此处已实现。

数据库固定分钟窗口在实例间共享：签发每账号 10 次/分钟，签发每连接来源 IP 60 次/分钟，兑换每 IP 60 次/分钟，校验及退出合计每 IP 120 次/分钟。限流包括失败请求，429 返回 `Retry-After: 60`。
IP 只取连接 `RemoteAddr`，不信任 X-Forwarded-For；反向代理/NAT 后可能共享额度，需要按部署验证容量，不应直接相信客户端头以规避限流。
每次限流事务按过期索引最多清理各表 1000 条过期记录；无游戏流量时过期摘要可留存，均不能使用，可由运维按 expires_at 清理。

服务不记录 JSON 请求、Authorization、Cookie、CSRF 或响应体；已知路径访问日志只记路径/状态/耗时。未知 game/launcher 路径和这些路由 panic 的值会脱敏，防止错误地将凭证写入路径或异常后落盘。
反向代理、数据库审计、APM 也必须禁止请求/响应正文、Cookie/Authorization 和带参数的 SQL 绑定值日志；服务端应用无法控制这些外部日志配置。游戏自身日志/模组/崩溃报告也需游戏方处理。

错误沿用 `{"error":{"code":...,"message":...,"request_id":...}}`：400 `invalid_game_request`；401 `game_unauthorized`；403 `game_forbidden`；429 `game_rate_limited`；503 `game_login_unavailable`。网站认证门禁还可返回既有 `unauthorized`/`invalid_csrf`/`auth_unavailable`。
旧部署可返回 404；本实现未配置返回 503；重放按契约用 401，不以 409 返回新会话。错误均是普通状态，不包含凭证或任意远端响应正文。

## 验证与部署验收

`make check` 运行静态检查和 race 测试；设置隔离测试库 `TEST_DATABASE_URL` 后，`make integration` 包括本功能的 PostgreSQL 与 HTTP 集成测试。测试库角色需遵循既有迁移测试的 CREATE DATABASE 要求。
HTTP 测试通过本地 OIDC 模拟签发器的真实授权码/PKCE/签名验证/UserInfo 流程获取 Cookie，并使用生产 PostgreSQL session store；这不能替代真实 Casdoor 部署测试。
测试覆盖单次码并发/重放、事务回滚、摘要存储、有效期、版本/协议/游戏/origin 绑定、CSRF、JSON/Host/Origin、网站退出、账号受限及恢复不复活、Bearer/Cookie 隔离、限流共享、只读存储及日志无凭证。

合并/发布前须在真实测试部署和真实账号补做：

1. Casdoor 登录后启动器签发、测试客户端兑换/校验身份一致；检查 HTTPS/Host/端口与服务器/客户端时钟。
2. 双并发只成功一次、重放和丢失响应失败；90 秒上限及网站/游戏会话期限。
3. 真实网站退出、账号限制/撤销后即时失效；外部 Casdoor 禁用/注销的运维同步路径。
4. 多实例/代理/NAT 限流和超时、网络故障、不跟随跨站重定向；停用配置/数据库只读时能力不可用。
5. 检索应用/代理/数据库/APM 日志和备份，确保无原码或游戏令牌（只有摘要）。
6. 待游戏方实现消费/脱敏后，再验证两种启动参数形式、异步兑换失败离线、stdout 跨块脱敏和崩溃报告/模组安全；全部验证后才能由游戏方加入能力声明。

本 PR 不执行部署、合并或生产配置修改，也不完成游戏或联机鉴权。
