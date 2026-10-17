# 作品发布与下载（第一部分）

本模块使用现有 Go HTTP 服务、PostgreSQL 与 Casdoor。临时操作页入口 `/projects`；业务 API 位于 `/api/v1`。页面没有独立前端工程、脚本构建或客户端授权逻辑。

## 安装、启动与运维

需要 Go 1.27.2+、Python 3、Docker Compose。云环境已恢复的工具链位于 `/workspace/.pvzce-tools/go/bin`，新环境须自行验证，不假设进程、配置或凭据仍存在。

```sh
export PATH=/workspace/.pvzce-tools/go/bin:$PATH
make workshop-init
make workshop-up
# 只给非 root 后端访问业务目录，不递归放宽凭据文件或 Casdoor 目录权限。
chmod 0755 data configs
# 等待数据库、Casdoor、S3 和 ClamAV 就绪。
# 直接运行时加载环境并覆盖容器内地址，禁止 set -x 或打印 .env。
set -a
. ./.env
set +a
export DB_HOST=127.0.0.1 DB_PORT=5432 DB_SSLMODE=disable CASDOOR_INTERNAL_URL=
go run ./cmd/server init-storage
go run ./cmd/server migrate
make run
```

`workshop-init` 保留已有 Casdoor 密钥和数据库凭据，生成本地 S3 凭据、稳定的 IP 哈希密钥及 `configs/s3.json`，均不输出秘密，文件权限为 0600。不要将 `.env`、配置凭据、上传内容、临时文件或 `work/` 提交 Git。

开发对象存储是 SeaweedFS 4.17 的 S3 API（可替换为 MinIO 或满足同一接口的 S3 服务）；ClamAV 使用 1.4 系列及受限配置。对象存储仅绑定本机 9000，ClamAV 仅绑定本机 3310；不要通过反向代理暴露这两个端口。存储节点内部其他 HTTP/gRPC 端口不发布到宿主机。S3 API 配置了凭据，无匿名访问身份。

完整容器启动前先执行上面的 `init-storage` 创建私有桶。随后：

```sh
docker compose -f compose.yaml -f compose.workshop.yaml up -d --build
```

服务器启动检查桶策略和 owner-only ACL，无法验证则拒绝启动；每次读写再次检查。任何非空桶策略都会被拒绝，包括可能安全但不受本平台管理的策略。启用 AWS S3 时也应启用 Object Ownership / Block Public Access，禁止对象公共 ACL。应用不会生成客户端对象存储地址或预签名下载地址；内部 ACL 检查使用的短期签名请求不会返回或写入日志。生产应用账号仅授予所需桶操作，使用 HTTPS；本地引导凭据具有开发桶管理能力，不能直接用于生产。

所有对象均位于私有桶的 `quarantine/` 下，是否可下载由数据库状态控制，不通过移动文件或更改桶公开权限实现发布。应用从不覆盖已上传的对象。同一版本仅允许一个包文件；失败包需要新建版本，扫描服务故障可重试原文件扫描。无论是否曾发布，版本号不能重复占用。已发布版本的文件、版本元数据和依赖不会原位更改。

上传先以私有临时文件流式接收，逐块计量和 SHA-256 校验，再执行受限检查、ClamAV INSTREAM 和 S3 写入。内存不随包体积线性增长；临时目录 `UPLOAD_TEMP_DIR` 默认 `cache/uploads`，单个请求最多占用一个文件上限的临时空间。容器工作坊配置把 cache tmpfs 扩至 512 MiB，应按并发量配置容量。结束即删除文件；维护任务清理超过 20 分钟的遗留 spool。对象存储写入后若数据库失败，会尝试删除孤立对象；删除失败的对象仍为私有，运维应按数据库引用对账清理，不能对整个 `quarantine/` 前缀设置简单过期删除，否则会删除已发布文件。

ClamAV 配置开启 `AlertExceedsMax`、加密文件提示，限制展开大小、递归、文件数和扫描时间。不得换用会静默跳过大文件并返回 OK 的宽松配置。引导镜像含真实签名库，配置要求启动时签名不超过 7 天；生产须通过受控 freshclam/签名分发持续更新并重启或重载 ClamAV，本地环境未把签名下载服务配置为外网例外。更新失败、签名库过旧无法启动、扫描缺失/断连/超时/无明确 OK，全部拒绝安全放行。未配置 `CLAMD_ADDRESS` 时上传直接返回 503，不接收文件；运行期间扫描失败的文件保存为 `failed`，不能提交发布或下载。只有明确 `stream: OK` 才记为 clean，发现病毒记为 infected，普通恢复和扫描重试都不能清除 infected。

云环境 Docker 构建使用挂载的合并 CA，保持代理与 TLS 验证；不会把会话 CA 放进镜像。受限网络若不能访问 Go 代理的存储跳转，可从被允许的官方 Git 源下载相同校验版本：

```sh
docker build --secret id=proxy_ca,src=/etc/ssl/certs/ca-certificates.crt \
  --build-arg DIRECT_MODULE_DOWNLOADS=github.com/klauspost/compress@v1.18.0 \
  -t pvzce-site:local .
```

## 配置

| 变量 | 默认值 / 要求 |
| --- | --- |
| `UPLOAD_MAX_FILE_BYTES` | 209715200 字节（200 MiB） |
| `UPLOAD_DAILY_QUOTA_BYTES` | 524288000 字节（500 MiB），账号、来源 IP 各自的上限 |
| `UPLOAD_QUOTA_TIMEZONE` | UTC，自然日；可设 IANA 名称，时区数据已嵌入静态二进制 |
| `UPLOAD_TEMP_DIR` | cache/uploads，私有、不可执行的临时卷 |
| `S3_ENDPOINT` / `S3_BUCKET` | S3 主机端口 / 已创建的私有桶 |
| `S3_ACCESS_KEY` / `S3_SECRET_KEY` | 服务端凭据，禁止放进响应、前端或日志 |
| `S3_SECURE` | true；开发配置显式为 false |
| `DOWNLOAD_HASH_KEY` | 至少 32 字节、跨重启稳定，HMAC 来源 IP；勿在一天中轮换以免切分 IP 额度 |
| `CLAMD_ADDRESS` | 内部 ClamAV TCP 地址；服务不得暴露给外网 |
| `TRUSTED_PROXY_CIDRS` | 空，默认忽略来源头；仅配置受控代理网段 |

接收连接只信任 `RemoteAddr`。仅当直接对端属于受控代理网段时，才从右向左解析 X-Forwarded-For 到第一个不受信任地址。无来源头时使用直接对端。不要信任整个互联网或未剥离客户端来源头的反向代理。日志只记录请求 ID、方法、路径、状态及响应字节，不记录来源头、查询、凭据和上传正文。

上传接口独立放宽请求体限制与读写期限（接收 10 分钟，响应最多 15 分钟），普通 JSON/表单沿用原请求上限。反向代理需允许相应文件大小、multipart 开销和时限，禁止自动解压上传内容。手动调大文件上限时，应同步评估临时卷、ClamAV 大小与展开限制、HTTP 时限及额度。

## 权限与账号

Casdoor 只管理凭据和已验证 OIDC 身份；业务表 `pvzce_site.accounts` 默认 active/user。任意已登录且未受限用户直接创建作品与版本，无作者名额/审批。作者由 owner 或 collaborator 关系决定，API 不接受客户端指定 owner、creator、role。

本站显示名和简介独立保存，不被重新登录覆盖。Casdoor 原用户名、邮箱及凭据不通过本站资料编辑接口修改。restricted 用户仍可注销，但业务操作被拒绝。公开内容读取仍开放。

首次管理员必须先通过真实 Casdoor 登录，取得 `/api/v1/account/me` 中的本站用户 ID，由受控主机上的操作员执行：

```sh
# 同前，加载数据库配置；不需要任何默认管理员密码。
go run ./cmd/server bootstrap-admin USER_ID
```

该命令只在本站还没有管理员时成功，写审计。不提供公开 HTTP 初始化入口。此后仅管理员通过管理接口授权角色或限制账号；不允许修改自己的角色/限制状态。Casdoor isAdmin 不映射本站管理员。审核人员和管理员都不能审核其拥有、协作或曾创建/上传/编辑的资源，移除协作者关系不会解除历史贡献者的利益冲突；申诉须由非原处置人、非作者的独立审核人员处理。

## 状态、策略与历史

作品与版本各自具有 draft → pending → published/rejected；作者可 withdraw → withdrawn，resume → draft。平台 remove → removed，restore → pending；恢复仍需安全文件和重新审核。编辑作品、替换图标会将作品变为 draft，原有包随作品状态退出公开。每次创建/内容编辑/图标更新保留不可变项目快照，状态操作、审核原因、申诉及管理操作均保留历史和审计，不硬删除历史。

提交/发布版本要求恰好一个 valid/clean 包；平台恢复也遵守该条件。作品图标须安全有效。作品与版本都 published、文件 valid/clean 才能公开下载。待审包不能经普通下载接口获取；独立审核人员可通过独立、鉴权、审计的 inspection 接口下载已扫描安全的待审文件。未扫描、失败或感染文件不提供 inspection。

内容策略 `content-v1`：允许非露骨成人暗示，须标注 suggestive，普通列表默认隐藏；禁止露骨内容、未成年人性化内容和政治内容。审核历史保存策略版本及具体理由。第一部分为人工判断，不使用关键词永久处罚或声称模型自动审核。审核人员发现错误标注应拒绝并要求修改。申诉可在页面/接口查询及回复，回复本身不直接恢复内容，恢复需走原状态机。

## 配额和幂等语义

作品包、图标和当前私密附件共同使用 `Begin / AuthorizeRead / Finish / Reap` 服务，UTC 自然日默认，界面展示实际配置、字节及日界线。单事务同时锁定账号和 IP 的 quota_days 行并预留整份声明长度，所有实例共享数据库；预留之前完成账号和资源授权。另预留一个检查越界的 guard 字节，正常 EOF 不计入实际用量，极限剩余额度可能需要多 1 字节可用预留空间。

每次读取前将最多 64 KiB 的授权读取量写入数据库，流结束按实际接收量结算，未收取的余额释放；哈希错误、拒审、删除、扫描失败都不返还已接收字节。进程崩溃时无法判断已授权的最后一个块实际到达多少，为避免少计，过期回收按持久化授权量保守计费，因此可能最多多计最后一个在途块（不超过 64 KiB）。预留最迟 15 分钟到期，并在当前配额自然日结束时提前到期；HTTP 接收期限也以日界线为上限，跨日须重开上传，不能用昨日预留继续收取新日流量。服务每分钟及新预留前回收有界批次。重启后旧上传不能继续提交。

`Idempotency-Key` 以账号为作用域，绑定作品、版本、用途、文件名、字节数及预期 SHA-256。同键完成重试返回同一文件、不重读正文、不重新计量；同键进行中/失败/过期或输入改变返回 409。客户端获得失败结果后使用新键发起新上传；正常中断已接收字节仍计费。额度以应用接收的文件内容为单位，不把普通表单字段和 HTTP/multipart 封装计入文件用量。

## API

成功响应 `{ "data": ... }`；写操作复用会话 Cookie、X-CSRF-Token 和原有 Origin 校验。JSON 拒绝未知字段。临时 URL-encoded 表单使用 csrf_token；流式 multipart 必须把 csrf_token 放在首字段，随后为元数据、最后为文件，避免为检查 CSRF 先整包缓冲。

| 方法和路径 | 行为 |
| --- | --- |
| GET `/api/v1/profile`；PATCH 同路径 | 读取本站资料 / 修改 display_name、bio |
| POST `/api/v1/projects`；PATCH `/api/v1/projects/{id}` | slug/title/kind/description/license/tags/content_label；kind 创建后固定 |
| GET `/api/v1/projects` | q/kind/tag/game_version/sort=updated\|downloads\|rating/page/size；稳定 ID 次序，最多 100/页、10000 页 |
| GET `/api/v1/author/projects` | 当前拥有/协作的作品，page 分页 |
| GET `/api/v1/projects/{id}` | 作品、可见版本、文件元数据、协作者；授权用户另见审核历史 |
| GET `/api/v1/projects/{id}/versions/{version}` | 指定可见版本详情 |
| GET `/api/v1/projects/{id}/revisions?before=ID` | 授权作者/审核人员的快照历史，游标分页 50 条 |
| GET `/api/v1/game-versions` | 已验证游戏版本、格式基线、只读参考提交 |
| POST `/api/v1/projects/{id}/versions` | number/changelog/channel=stable\|beta/game_versions/dependencies |
| POST `/api/v1/projects/{id}/collaborators` | 所有者管理 user_id/remove |
| POST `/api/v1/projects/{id}/icon` | 选取本作品 valid/clean 图标 file_id 并重审 |
| PUT `/api/v1/projects/{id}/uploads/{purpose}?version_id=ID` | 原始流上传，purpose=package\|icon\|attachment；仅 package 指定版本 |
| GET `/api/v1/upload-usage` | 字节、账号已用/预留额度、上限及日界线 |
| GET/HEAD `/api/v1/files/{file}/download` | 公开安全包/图标，或资源授权的私密附件；HEAD 不计数 |
| POST `/api/v1/files/{file}/rescan` | 重试 failed 扫描；不会发布或清除 infected |
| POST `/api/v1/projects/{id}/transitions` | action/reason；作品状态操作 |
| POST `/api/v1/projects/{id}/versions/{version}/transitions` | 相同协议；版本独立操作 |
| GET `/api/v1/review-queue` | 审核权限，最早 100 个待审作品/版本 |
| GET `/api/v1/review/files/{file}` | 独立审核人员的安全文件审计下载 |
| GET/POST `/api/v1/appeals` | 查询 / review_id+reason 申诉 |
| POST `/api/v1/appeals/{appeal}/resolve` | 独立审核人员 reply；不自动恢复内容 |
| POST `/api/v1/admin/accounts/{user}` | 管理员 role/status/reason |
| GET `/api/v1/admin/audit` | 管理员最近 100 条审计 |

原始上传必须提供准确 Content-Length、Upload-Filename、Upload-SHA256（小写十六进制）和 Idempotency-Key，匿名不可上传。SHA-256 由服务端重新计算并比较。上传返回 200 表示已持久化接收结果，**不等于安全或发布**，客户端必须展示 scan_state、validation_state、元数据中的失败原因与后续人工审核状态。超文件上限 413，额度不足 429，未配置服务 503，幂等或文件槽冲突 409。

版本依赖对象：`{"version_id":123,"kind":"required"}`，支持 optional/conflict，仅引用已公开版本；依赖记录不可变。包内 depends/breaks 保留在文件元数据，PVZCE 范围另做结构/兼容校验，不能用平台依赖元数据掩盖包内不兼容。下载响应带 X-Checksum-SHA256、Content-Length 和 attachment 文件名，不把包当 HTML 执行，不提供对象 Key。范围请求当前返回完整文件。

去重规则：有效包对象打开成功后，按文件 + UTC 日期 + HMAC 来源 IP 计一次 GET，事务同时维护项目和版本计数；HEAD、图标、附件不计。连接中断后不能可靠证明客户端是否收到全部字节，因此统计是获准开始的下载，不宣称完成下载人数。同一 NAT 当日共用一次计数。

## 迁移与第二部分接口

嵌入的 `internal/migrate/sql` 按文件名排序，数据库咨询锁串行执行，SHA-256 校验已应用文件，事务失败整体回滚。已有未版本化的 `pvzce_auth` 表可由 001 接管，不丢账户或会话。002 建立业务结构，003 补历史/策略版本/标签字典，004 建立 pg_trgm 和排序索引（PostgreSQL 18 自带扩展包，部署账号需有安装该 trusted extension 的权限）。升级先备份数据库和私有桶，在维护窗口执行 migrate（大型库可为迁移进程增大 DB_QUERY_TIMEOUT，例如 2m），再部署应用；未来只添加新迁移，不编辑已应用迁移。不提供破坏性 down。服务启动自动运行同一迁移机制，多个实例可并发安全启动。

第二部分应添加后续迁移和服务：

- ratings 按 user_id/project_id 唯一，评分更新与 projects.rating_sum/rating_count 同事务维护；当前字段为真实的 0，排序已预留，没有占位评分。
- 评论、模型决策、反馈、举报等复用 actor、member、审计及当前 transitions，不能复制一个更宽松的授权/发布状态机。模型只给建议；第一部分未接入模型。
- `Begin/AuthorizeRead/Finish/Reap` 可由反馈上传复用，同一账号/IP/自然日账本；ObjectStore 和 Scanner 契约可替换。当前 attachment 是作品范围的私密 text/log/PNG/JPEG，必须保持 clean/valid 后授权下载。第二部分需增加反馈资源绑定及其公开/私密 ACL（游戏反馈未必有作品），**不能直接把反馈附件暴露给全部作品协作者或通过当前公开包下载分支**。扩展文件关联时仍在同一事务检查资源归属。
- 举报恶意文件应设置安全隔离状态，任何普通 restore 都不能把 infected/failed 文件转 clean；安全裁决与内容恢复保持独立。
- reviews.policy_version 和 project_revisions 已可用于追溯；新增自动审核需要单独记录模型/规则版本、分类、理由、置信度和人工覆盖，不能把未接通服务标成自动安全。
