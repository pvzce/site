# pvzce-site 后端

本项目依据 GNU 通用公共许可证第 3 版（GPL-3.0-only）开源。许可证全文见 [`LICENSE`](LICENSE)。

基于 Go 1.27 标准库 `net/http` 的后端，使用 PostgreSQL 连接池、受限目录读取和 Casdoor OIDC 账户登录。可编译为单个静态二进制，也可通过 Docker Compose 启动。

已包含：版本化 API、健康检查、结构化日志、请求限制、优雅停机、PostgreSQL 连接池与事务、内部文件读取、Casdoor 登录 / 注册入口、持久化会话、账户页、注销、自动测试和 CI。

## 本地 Casdoor 和账户系统

需要 Docker Engine、Docker Compose 和 Make。启动整个本地环境：

```bash
make up
```

该命令构建后端镜像，自动生成 `.env` 中缺少的凭据和 `configs/casdoor/` 配置，然后启动 PostgreSQL、Casdoor 和后端。宿主机无需安装 Go。已使用固定版本 `casbin/casdoor:4.18.0`，遵循 [Casdoor 标准 OIDC 接入方式](https://casdoor.ai/docs/how-to-connect/oidc-client/)。

- 本站账户页：`http://localhost:8080/account`
- 本站登录入口：`http://localhost:8080/auth/login`
- Casdoor 管理页面：`http://localhost:8000`
- 测试账户：组织 `pvzce`，用户名 `demo`
- 管理账户：组织 `built-in`，用户名 `pvzce-admin`
- 初始密码：保存在 `configs/casdoor/credentials.txt`，未提交到 Git

请统一使用 `localhost` 访问这些地址，确保回调地址和浏览器 Cookie 的主机一致。Casdoor 的登录页同时提供普通账户注册；用户资料和密码由 Casdoor 管理。默认内置 `admin` 被禁用，本地管理员和测试用户使用随机生成的密码。

生成文件均为 `0600`，配置目录为 `0700`。初始化命令可以重复执行，会保留原有数据库密码、客户端密钥和签名证书。Casdoor 使用合并导入，用户修改密码后不会在重启时被覆盖；`credentials.txt` 只记录初始密码。

```bash
make auth-init                   # 单独生成 / 更新本地 Casdoor 配置
docker compose ps
docker compose logs -f backend casdoor
make down                        # 停止服务，保留数据库卷
```

如需使用其他 Compose 可执行文件：`make up COMPOSE=/path/to/docker-compose`。调整端口时同步修改 `PORT` / `CASDOOR_PORT`、`APP_PUBLIC_URL` / `CASDOOR_ISSUER_URL`，执行 `make auth-init` 后重新创建 `casdoor` 和 `backend` 容器以加载更新的配置：

```bash
docker compose up -d --build --force-recreate casdoor backend
```

Casdoor 在 PostgreSQL 内使用独立的 `casdoor` 数据库，本站账户及会话使用业务数据库中的 `pvzce_auth` schema。启动时自动创建账户系统表，并使用数据库锁避免并发初始化冲突。

## 登录流程与前端调用

浏览器访问 `/auth/login`，后端创建一次性登录状态并跳转到 Casdoor。回调 `/auth/callback` 兑换授权码，校验应用专用 JWKS 的 RS256 签名、issuer、audience、有效期和 nonce，并核对 UserInfo 的 subject。流程使用 state、浏览器绑定 Cookie 和 PKCE S256。

认证后，浏览器只保存随机的 `pvzce_session` Cookie，带有 HttpOnly 和 SameSite=Lax 属性；HTTPS 公共地址会自动启用 Secure。数据库保存会话令牌的 SHA-256 哈希和过期时间，默认会话有效期 24 小时。会话可跨后端重启，并能通过注销立即撤销。Casdoor 的访问令牌和客户端密钥均不返回前端。

同源前端示例：

```javascript
// 跳转至 Casdoor 登录，成功后进入 /account。
window.location.href = "/auth/login";

// 获取当前用户和 CSRF 令牌。未登录返回 401。
const response = await fetch("/api/v1/account/me", { credentials: "same-origin" });
const { data: session } = await response.json();

// 注销本站会话。
await fetch("/auth/logout", {
  method: "POST",
  credentials: "same-origin",
  headers: { "X-CSRF-Token": session.csrf_token },
});
```

`POST /auth/logout` 校验 CSRF 令牌和请求来源，JSON / API 调用成功返回 204，账户页表单提交成功跳转回 `/account`。GET 注销返回 405。注销作用于本站会话；Casdoor 的 SSO 会话可在 Casdoor 页面退出。

新增受保护的业务路由时使用 `httpapi.RequireAccount(deps.Auth, handler)`，处理器通过 `httpapi.Account(r.Context())` 读取当前账户。该包装器也会校验写请求的 CSRF 令牌。项目权限应在业务层单独定义，Casdoor 管理员身份不会自动赋予本站业务权限。

公共健康检查和 `/api/v1/info` 保持开放；文件工具仍只供内部使用。未配置账户服务时，账户接口返回 503，普通后端功能可以独立运行。启用账户系统时必须配置数据库和客户端密钥。

## 本地开发

需要 Go 1.27 或更新版本，以及 Make。首次启用账户系统建议先执行上面的 `make up`。

```bash
test -f .env || cp .env.example .env
# 已启用 AUTH_ENABLED 时，需要运行 PostgreSQL 和 Casdoor。
make run
```

默认地址：`http://localhost:8080`。

```bash
curl http://localhost:8080/api/v1/info
curl http://localhost:8080/health/ready
```

`make run` 会读取 `.env`。直接执行 `go run ./cmd/server` 或二进制时，请通过进程环境传入配置；程序自身不会自动加载 `.env`。

当前工作区已生成 `.env`，含随机数据库密码、权限为 `0600`，已被 Git 忽略；保留现有文件即可。新克隆项目时复制示例文件，并填入随机 `DB_PASSWORD`，例如使用 `openssl rand -hex 24` 生成。直接运行时，未设置 `DATABASE_URL` 或 `DB_HOST` 会跳过数据库连接，便于单独开发 HTTP 接口。

本地连接配套 PostgreSQL：

```bash
make db-up
# 在 .env 设置 DB_HOST=127.0.0.1，再启动服务
make run
```

`make db-up` 使用 `compose.dev.yaml`，只将数据库发布到 `127.0.0.1:5432`，可通过 `.env` 的 `DB_PORT` 调整。应用在 Compose 中始终使用内部端口 5432。

开发账户接口时，保持 Casdoor 运行，在 `.env` 设置 `DB_HOST=127.0.0.1`、`CASDOOR_INTERNAL_URL=`，并先执行 `docker compose stop backend` 释放 8080 端口，再执行 `make run`。单独开发其他接口时可设置 `AUTH_ENABLED=false`。

## Docker 部署

需要 Docker Engine 和 Docker Compose。首次运行先执行 `make auth-init` 生成 Casdoor 配置，之后可启动，无需在宿主机安装 Go：

```bash
docker compose up -d --build
docker compose ps
docker compose logs -f backend
```

Compose 会启动 PostgreSQL 18 和 Casdoor，在依赖健康后启动后端。数据库使用命名卷持久化，默认不发布数据库端口。应用与 Casdoor 端口默认绑定宿主机回环地址。Compose 默认使用生产环境；部署到域名时，将 `APP_PUBLIC_URL`、`CASDOOR_ISSUER_URL` 设为相应的 HTTPS 地址，通过已有反向代理转发。

调整宿主机端口使用 `PORT`。Compose 内部固定监听 `:8080`，不使用 `.env` 中的 `HTTP_ADDR`；该变量供本地开发和直接运行二进制使用。

`DATA_DIR` 和 `CONFIG_DIR` 分别以只读方式挂载到容器的 `/data` 和 `/configs`，宿主机目录必须已存在。缓存使用 `/cache` 的 64 MiB tmpfs，属于容器用户 `65532:65532`，容器停止后内容丢失。数据、实际配置和缓存内容不进入镜像。参考 [Docker tmpfs 文档](https://docs.docker.com/engine/storage/tmpfs/)。

若配置了 `DATABASE_URL`，连接地址会优先于配套数据库设置；配套数据库服务仍会启动，也可选择下面的独立镜像方式运行。

更新与停止：

```bash
docker compose up -d --build
docker compose down
```

容器以非 root 用户运行，文件系统只读，内置健康检查，日志输出到标准输出并限制轮转大小。最终镜像使用 `scratch`，只包含静态程序及 CA 证书。停机宽限期为 30 秒；如果调大 `SHUTDOWN_TIMEOUT`，同步增大 `compose.yaml` 的 `stop_grace_period`，使其超过应用的停机超时。

`docker compose down` 保留数据库卷。已有数据库初始化后，修改 `.env` 的密码不会自动修改数据库角色密码，应通过 SQL 修改并同步应用配置。数据库卷的备份与恢复可使用 `pg_dump` / `pg_restore`。

不使用 Compose 时也可以运行：

```bash
docker build -t pvzce-site:local .
docker run -d --name pvzce-site \
  --read-only --cap-drop=ALL --security-opt=no-new-privileges \
  --restart=unless-stopped --stop-timeout=30 \
  --env-file .env -e DATA_DIR=/data -e CONFIG_DIR=/configs -e CACHE_DIR=/cache \
  -v "$(pwd)/data:/data:ro" \
  -v "$(pwd)/configs:/configs:ro" \
  --tmpfs /cache:rw,noexec,nosuid,nodev,size=64m,mode=0750,uid=65532,gid=65532 \
  -p 127.0.0.1:8080:8080 pvzce-site:local
```

独立镜像不启动配套数据库；需要连接数据库时，在 `.env` 的 `DATABASE_URL` 使用容器可访问的地址。`127.0.0.1` 在容器内指向容器自身。

## 直接部署二进制

```bash
make build VERSION=0.1.0
APP_ENV=production HTTP_ADDR=:8080 ./bin/server
```

可将 `bin/server` 复制到相同系统和架构的服务器，准备 `data`、`configs` 目录，或设置 `DATA_DIR`、`CONFIG_DIR` 指向已有目录。`CACHE_DIR` 不存在时会自动创建。通过 systemd 或其他进程管理器运行。Linux 交叉编译示例：

```bash
GOOS=linux GOARCH=amd64 make build VERSION=0.1.0
# ARM64 服务器改为 GOARCH=arm64
```

程序收到 `SIGINT` 或 `SIGTERM` 后会取消就绪状态、停止接收新连接，并等待当前请求结束；超过停机时限后关闭连接。内置探针可用于外部进程管理器：

```bash
HTTP_ADDR=:8080 ./bin/server healthcheck
```

探针请求 `/health/ready`，成功退出码为 0，失败为 1。

## 目录结构

```text
cmd/server/            程序入口、信号处理、健康检查命令
internal/buildinfo/    服务名与构建版本
internal/auth/         OIDC 客户端、登录状态、账户和会话存储
internal/casdoorbootstrap/ 本地 Casdoor 配置与凭据初始化
internal/authtest/     OIDC 协议测试用提供方和会话存储
internal/config/       环境变量配置及启动校验
internal/database/     PostgreSQL 连接池、查询超时、事务与集成测试
internal/fileio/       按数据、配置、缓存分类的受限目录读取
internal/httpapi/      路由、中间件、JSON 编解码和接口测试
internal/server/       HTTP 服务器与优雅停机
internal/healthcheck/  容器健康检查客户端
.github/workflows/     格式、静态检查、竞态测试、镜像构建
Dockerfile             静态程序的多阶段构建
compose.yaml           后端、PostgreSQL 和 Casdoor 部署配置
compose.dev.yaml       宿主机开发用数据库端口
data/                  业务数据目录，内容不提交
configs/               业务配置目录，可提交 *.example.* 示例
configs/casdoor/       本地 Casdoor 配置、签名证书及初始凭据（忽略提交）
cache/                 可重建缓存目录，内容不提交
tests/casdoor_e2e.py    真实本地 Casdoor 登录链路验证
```

后续业务接口在 `internal/httpapi/router.go` 注册；处理器可以拆成独立文件。业务逻辑按功能建立 `internal/<业务模块>`。`httpapi.Dependencies` 提供共享的 `Database`、`Files` 和 `Auth`，由程序入口初始化并在 HTTP 服务停机后关闭。业务表及具体业务权限由后续模块定义。

## 文件分类

| 目录 | 环境变量 | 用途 | 生命周期 |
| --- | --- | --- | --- |
| `data/` | `DATA_DIR` | 数据文件、业务资源、导入 / 导出文件 | 持久保留，按业务需要备份 |
| `configs/` | `CONFIG_DIR` | 业务配置 JSON、规则、模板配置 | 持久保留，通过部署更新 |
| `cache/` | `CACHE_DIR` | 可重新生成的计算结果、临时缓存 | 可清理，容器停止后自动丢弃 |

程序环境变量仍由根目录 `.env` 管理，业务配置文件放入 `configs/`。PostgreSQL 数据由独立的命名卷持久化，日志输出到标准输出。

本地运行时，数据和配置目录必须已存在，缺少缓存目录会创建为 `0750`（受进程 umask 影响）。这三个目录必须彼此独立；相同目录、父子嵌套和指向同一目录的符号链接别名都会导致启动失败。读取工具不提供缓存写入、过期或自动淘汰功能，缓存生成策略由后续业务模块实现。

实际数据、实际配置和缓存文件均被 Git 忽略，目录中保留 `.gitkeep`。配置示例可使用 `configs/*.example.*` 命名并提交。镜像构建只保留目录占位文件，运行时通过挂载提供文件。

## 数据库工具

设置 `DATABASE_URL` 或 `DB_HOST` 后启用数据库。应用在监听 HTTP 端口前检查连接；连接失败会阻止启动。默认最多 10 个连接、最少 1 个连接，并配置连接寿命、空闲时间及查询超时。复用同一个 `*database.DB`，通过 `$1` 等占位符传递值。

```go
var count int
err := deps.Database.QueryRow(ctx, "SELECT count(*) FROM items WHERE owner_id = $1", ownerID).Scan(&count)

rows, err := deps.Database.Query(ctx, "SELECT id FROM items WHERE owner_id = $1", ownerID)
if err != nil {
    return err
}
defer rows.Close()
for rows.Next() {
    // rows.Scan(...)
}
if err := rows.Err(); err != nil {
    return err
}

err = deps.Database.InTx(ctx, func(txCtx context.Context, tx pgx.Tx) error {
    _, err := tx.Exec(txCtx, "INSERT INTO items (name) VALUES ($1)", name)
    return err
})
```

`Exec`、`Query`、`QueryRow` 和 `InTx` 都设置默认截止时间；已有更短的 context 截止时间仍优先。`Query` 的截止时间覆盖结果读取，必须关闭结果集；`QueryRow` 必须调用 `Scan`。事务回调成功会提交，返回错误或 panic 会回滚。`Stats` 提供连接池统计。

以上是业务层使用示例，`items` 表由后续业务迁移创建。未配置数据库时 `deps.Database` 为 nil，依赖数据库的业务模块应在初始化时检查该条件。

远程实例可通过 `DATABASE_URL` 配置，例如 `postgres://user:password@db.example.com:5432/app?sslmode=verify-full`。URL 中的密码需进行 URL 编码；使用单独的 `DB_*` 变量时，程序会自动编码密码。默认 `DB_SSLMODE=require`，示例文件和配套 Compose 使用 `disable` 连接本地数据库。

## 内部文件工具

`deps.Files.Data`、`deps.Files.Config`、`deps.Files.Cache` 分别提供独立的 `ReadFile`（字节）、`ReadText`（UTF-8 文本）、`ReadJSON` 与 `Open`。也可以在其他内部模块通过 `fileio.New("./data", 16 << 20)` 创建并关闭独立的读取工具。

```go
body, err := deps.Files.Data.ReadFile(ctx, "assets/example.bin")
text, err := deps.Files.Data.ReadText(ctx, "messages/welcome.txt")
var settings struct {
    Title string `json:"title"`
}
err = deps.Files.Config.ReadJSON(ctx, "settings.json", &settings)
cached, err := deps.Files.Cache.ReadFile(ctx, "computed/result.json")

file, err := deps.Files.Data.Open(ctx, "assets/example.bin")
if err != nil {
    return err
}
defer file.Close()
```

文件名必须为根目录内的相对路径。工具拒绝绝对路径、`..`、非普通文件和超过上限的文件，阻止符号链接逃逸；默认上限 16 MiB。文件读取会在每次读取之间检查 context，JSON 解码后的业务字段由调用方校验。`Open` 返回文件句柄，调用方负责关闭；文件大小在打开时校验，整文件读取还会校验实际读取大小。

每个类别的相对路径都从自己的目录解析；例如配置 `settings.json` 位于 `configs/settings.json`，数据读取工具无法通过符号链接读取它。

文件只供内部模块读取，没有文件下载或上传接口。

新增 JSON 写接口时，使用 `decodeJSON` 检查类型、语法、未知字段和请求体大小，再校验业务字段。中间件拒绝超过上限的已知长度请求，并限制未知长度请求可读取的字节数；未知长度请求必须通过受限的 `r.Body` 读取，才能检测超限并返回 413。

## 接口约定

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET / HEAD | `/` | 服务信息 |
| GET / HEAD | `/api/v1/info` | 服务名、版本、环境 |
| GET / HEAD | `/health/live` | 存活状态 |
| GET / HEAD | `/health/ready` | 就绪状态，未就绪返回 503 |
| GET | `/account` | 账户页及登录入口 |
| GET | `/auth/login` | 创建登录流程并跳转 Casdoor |
| GET | `/auth/callback` | 校验授权码并创建本站会话 |
| GET / HEAD | `/api/v1/account/me` | 当前账户和 CSRF 令牌，需登录 |
| POST | `/auth/logout` | 撤销本站会话，需 CSRF 校验 |

就绪条件是 HTTP 服务已启动、未开始停机，且已配置的数据库 Ping 成功。数据库检查最多等待 1 秒；不可用时 `/health/ready` 返回 503，`/health/live` 不依赖数据库。

成功响应示例：

```json
{"data":{"name":"pvzce-site","version":"dev","environment":"development"}}
```

错误响应示例：

```json
{"error":{"code":"not_found","message":"Route not found","request_id":"..."}}
```

每个请求都会生成 `X-Request-ID`，用于关联响应与日志。客户端传入的同名头不会作为日志 ID。日志为 JSON，包含方法、路径、状态码、耗时和响应字节数；不记录请求体或查询参数。健康检查成功日志使用 DEBUG 级别。

## 环境配置

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `APP_ENV` | `development` | `development` / `production` / `test`；容器默认 `production` |
| `HTTP_ADDR` | `:8080` | 监听地址 |
| `LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `HTTP_READ_HEADER_TIMEOUT` | `5s` | 读取请求头超时 |
| `HTTP_READ_TIMEOUT` | `15s` | 读取整个请求超时，不得小于请求头超时 |
| `HTTP_WRITE_TIMEOUT` | `15s` | 响应写入超时 |
| `HTTP_IDLE_TIMEOUT` | `60s` | 空闲连接超时 |
| `SHUTDOWN_TIMEOUT` | `10s` | 优雅停机等待时间 |
| `HTTP_MAX_BODY_BYTES` | `1048576` | 请求体上限，默认 1 MiB |
| `DATABASE_URL` | 空 | 完整 PostgreSQL URL，优先于 `DB_HOST` 等变量 |
| `DB_HOST` | 空 | 直接运行时，设置该项以启用数据库；Compose 内固定为 `database` |
| `DB_PORT` | `5432` | 本地连接端口与开发时的宿主机数据库端口 |
| `DB_NAME` / `DB_USER` | `pvzce` | 数据库名与用户名 |
| `DB_PASSWORD` | 空 | 数据库密码；Compose 必填 |
| `DB_SSLMODE` | `require` | 本地示例和 Compose 使用 `disable` |
| `DB_MAX_CONNS` / `DB_MIN_CONNS` | `10` / `1` | 连接池上下限，最小值不得超过最大值 |
| `DB_CONNECT_TIMEOUT` | `5s` | 启动连接及单次建连超时 |
| `DB_QUERY_TIMEOUT` | `3s` | 查询、读取结果及事务超时 |
| `DB_MAX_CONN_LIFETIME` | `1h` | 连接最长寿命，附加随机抖动避免同时重连 |
| `DB_MAX_CONN_IDLE_TIME` | `5m` | 连接最长空闲时间 |
| `DATA_DIR` | `./data` | 已存在的业务数据目录 |
| `CONFIG_DIR` | `./configs` | 已存在的业务配置目录 |
| `CACHE_DIR` | `./cache` | 本地缓存目录，不存在时创建；Compose 固定使用 `/cache` tmpfs |
| `FILE_MAX_BYTES` | `16777216` | 文件大小上限，允许 1 字节至 1 GiB |
| `AUTH_ENABLED` | `false` | 启用 Casdoor 账户系统，初始化命令会设置为 true |
| `APP_PUBLIC_URL` | `http://localhost:8080` | 浏览器访问本站的公共地址 |
| `CASDOOR_ISSUER_URL` | `http://localhost:8000` | Casdoor 公共地址，也是严格校验的 issuer |
| `CASDOOR_INTERNAL_URL` | 空 | 后端到 Casdoor 的内部地址；Compose 设置为 `http://casdoor:8000` |
| `CASDOOR_APPLICATION` / `CASDOOR_CLIENT_ID` | `pvzce-site` | Casdoor 应用名称与客户端 ID |
| `CASDOOR_CLIENT_SECRET` | 空 | 初始化生成的随机应用密钥 |
| `AUTH_SESSION_TTL` | `24h` | 本站会话有效期，允许 1 分钟至 30 天 |
| `CASDOOR_PORT` | `8000` | Casdoor 宿主机端口 |

兼容旧变量 `FILE_ROOT`：只在未设置 `DATA_DIR` 时用于指定数据目录；新配置请使用 `DATA_DIR`。

配置错误会阻止启动。HTTP 超时控制网络读写；业务中的数据库查询或外部请求还应使用有截止时间的 context。长连接、流式响应或大文件上传需要按接口需求调整这些设置。

Compose 额外使用 `HOST`、`PORT`、`VERSION`，分别控制宿主机绑定地址、端口、镜像标签与构建版本。镜像内的健康检查不受宿主机端口变化影响。

## 验证与性能

```bash
make fmt
make check
make build
TEST_DATABASE_URL='postgres://user:password@localhost:5432/testdb?sslmode=disable' make integration
python3 tests/casdoor_e2e.py      # 需本地 Compose 服务已运行
go test -run '^$' -bench BenchmarkInfo -benchmem ./internal/httpapi
```

测试覆盖路由与 HEAD 行为、错误格式、配置校验、请求体限制、异常恢复、健康检查、正常停机、文件大小与目录限制、类别间隔离、目录重叠与符号链接别名、缓存目录创建、文本及 JSON 读取。设置 `TEST_DATABASE_URL` 后还会运行真实数据库的参数化查询、事务提交 / 回滚、查询超时、连接池恢复和就绪状态测试；未设置时仅跳过这些集成测试。

数据库集成测试连接专用测试库，验证事务、账户会话的持久化、令牌哈希、注销及过期。协议测试使用本地 OIDC 提供方，覆盖错误签名、错误 issuer / audience / nonce、过期令牌、UserInfo 身份不匹配、PKCE、回调重放与 CSRF。CI 启动独立 PostgreSQL，运行包含集成测试的竞态检查并构建 Docker 镜像。

`tests/casdoor_e2e.py` 使用本地 `demo` 账户验证真实 Casdoor 授权码、本站 Cookie、账户接口、回调重放防护和注销，不输出密码或令牌。测试账户改密后，可通过 `CASDOOR_E2E_PASSWORD` 指定新的密码。

基准测试用于观察处理器耗时与内存分配，包含测试用响应记录器的开销，不能当作部署后的 QPS。实际吞吐量取决于服务器资源、业务逻辑、数据库和日志量，应在业务接口完成后进行并发压测。
