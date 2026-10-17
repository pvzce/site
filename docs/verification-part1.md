# 第一部分验证

在现有任务 checkout 上从远程 main `1777768` 快进后开发，没有额外 worktree；游戏仓库只读参考提交为 `591acf5a60455e56f26953c2854c8eeb2928aef4`。未复用不存在的旧进程或凭据：恢复 Go 1.27.2，重新生成本地配置，实际启动 PostgreSQL 18、Casdoor 4.18.0、SeaweedFS S3 4.17 和 ClamAV 1.4.6。

## 可重复执行的检查

加载已生成的 `.env`，直接运行时设置 DB_HOST=127.0.0.1、DB_PORT=5432、DB_SSLMODE=disable、CASDOOR_INTERNAL_URL=。TEST_DATABASE_URL 指向单独的测试库，不能指向生产；测试会创建/删除带随机前缀的记录，迁移升级测试另外创建独立随机数据库。

```sh
# 不打印连接串或凭据，不使用 set -x。
# TEST_DATABASE_URL、TEST_CLAMD_ADDRESS、TEST_S3_ENDPOINT 由受控环境传入。
go test -race ./...
go vet ./...
make build
python3 tests/casdoor_e2e.py

# 在可丢弃的本地环境中注册独立审核测试账号，不输出凭据。
python3 tests/workshop_e2e.py --prepare
# 首次在本机用上一步打印的现有 OIDC 用户 ID 初始化本站管理员。
go run ./cmd/server bootstrap-admin USER_ID
python3 tests/workshop_e2e.py
```

`go test` 未设置真实服务变量时会明确跳过相应 integration，用模拟提供方的协议测试不能替代真实登录。生产启动不会使用测试 scanner/object store。

| 检查 | 本次结果与边界 |
| --- | --- |
| 原有测试 + go test -race | 通过，配置真实 PostgreSQL 测试库；包含原 OIDC、会话、CSRF、数据库、服务及文件工具测试 |
| 迁移升级 | 通过：旧 auth 表/账户保留、并发 Apply、重复执行、校验和漂移拒绝；测试库内创建独立迁移数据库 |
| 包格式 | 通过：真实模组模板元数据、PVZCE 数据包目录、兼容范围、错误平台/版本、损坏 JSON、旧字段、路径穿越/特殊路径/重复名/符号链接、条目限制、压缩炸弹、图标完整解码 |
| 配额 | 通过：多账号共享 IP 并发原子限制、账号跨 IP 限制、实际字节结算、中断、过期在途块记账、过期不能继续、相同键成功重试/进行中冲突/指纹变化拒绝、包/图标/附件共享同一总量 |
| 授权和状态 | 通过：跨作者修改/上传拒绝、角色提权拒绝、自审/协作者审核拒绝、独立申诉处理、账号限制、撤回恢复、下架、编辑重审、文件不可替换、私密附件防越权 |
| 搜索与分页 | 通过：关键词/标签/分类/游戏版本、稳定排序、分页和排序注入拒绝；评分字段预留为 0，真实评分由第二部分实现 |
| S3 真实服务 | 通过：鉴权读写、真实字节往返、匿名对象读取 403、owner-only ACL 检查；SeaweedFS 当前版本拒绝本测试的 policy mutation，非空策略的 adapter 拒绝另有独立 HTTP 测试，未声称真实存储策略修改试验成功 |
| ClamAV 真实服务 | 通过：普通文件 clean、EICAR infected；断连/未配置/超时的失败保持隔离另有故障测试 |
| 真实 Casdoor | 通过：signup、登录授权码、应用 JWKS、Cookie、CSRF、回调重放拒绝、注销；独立注册用户经本站管理员接口授权 reviewer |
| 真实作品闭环 | 通过：普通 Go 模板/URL-encoded/multipart 表单 → 创建版本 → 上传 → 隔离普通下载 → 独立审核 → 匿名下载并核对 SHA-256、重复下载去重 → 撤回/重新审核；EICAR ZIP 上传隔离，提交和普通恢复被拒绝 |
| go vet / 静态构建 | 通过 |
| Docker 镜像 | 通过：Go 1.27.2 构建、最终 scratch 非 root 镜像；云代理 CA 仅在下载步骤挂载，受限模块从官方 Git 源校验下载 |

真实闭环测试构造的包在内存生成，不提交上传 ZIP 或病毒样本到 Git。测试账户凭据在忽略的 work/ 中，实际 API 返回/页面和服务日志未输出密钥。一次完成的公开包核对 SHA-256 为 `d5b784f2e5bdaa3d0baddcb08a85a2ffe48a69570fd2953a2f4bbccf1eb0eef8`；每次新运行 ZIP 时间戳可能不同，脚本总是比较当次真实内容。

未执行游戏程序/Java 内容 codecs，未声称运行语义兼容已验证；没有独立前端构建、自动模型审核或第二部分评论评分逻辑。生产管理员、域名、生产 S3/数据库凭据和持续签名更新仍需部署方配置。本任务不把本地开发服务当成生产上线。
