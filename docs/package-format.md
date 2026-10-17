# PVZCE 包格式核对与平台校验边界

只读参考仓库 `pvzce/pvzce`，核对提交 `591acf5a60455e56f26953c2854c8eeb2928aef4`。本任务没有修改游戏仓库。以下来自游戏源代码及真实模板，不由 Minecraft、Modrinth 的包协议推断：

| 依据 | 已核实事实 |
| --- | --- |
| `gradle.properties` | 游戏项目版本 1.0.0；Java 25 |
| `AGENTS.md`、`docs/mod-guide/README.md` | 独立桌面游戏，裁剪的 Fabric Loader 分支；无 remap、mixin、访问宽限支持假定 |
| `pvzce-mod-template/src/main/resources/fabric.mod.json` | schemaVersion=1、id=example_mod、version=1.0.0、environment=*、depends.pvzce=* |
| `pvzce-loader/.../metadata/V1ModMetadataParser.java` | 实际加载器的 Fabric 元数据解析，不是 Modrinth 平台清单 |
| `pvzce-loader/.../launcher/PvzceGameProvider.java` | 独立 PVZCE 游戏提供方及游戏版本身份 |
| `PvzceResourceManager.java` | resourcepacks/datapacks 仅发现直接子目录并按名字排序；不是直接加载 ZIP |
| `DirectoryPack.java`、`PackPaths.java` | 从包根读取 data/assets 路径；完整相对路径参与资源标识 |
| `PvzceDataLoader.java` | `data/<namespace>/<registry>/<path>.json`；植物、僵尸、关卡、粒子等注册表；关卡旧 conveyor/placement_zone 须迁至 mechanics |
| `pvzce-game/src/main/resources/pack.mcmeta` | 内置包 pack_format=1、字符串描述 |
| 内置 data/c/tags 与 data/pvzce 内容 | 实际目录没有额外重复的 pvzce 层级，标签接受对应单/复数注册表名称 |

游戏代码当前读取 pack.mcmeta 的字符串描述，缺失/损坏时退回目录名；尚未强制 pack_format 或解析包依赖。本站的上传约束是分发平台更严格的结构契约，不宣称这些约束已由游戏执行。

模组：根目录 fabric.mod.json，schema 1、合法 Fabric ID、版本与发布版本相同，要求 depends.pvzce 且所选游戏版本满足范围；明确带 depends.minecraft 的包拒绝。保留 depends/breaks 原始元数据。支持 `*`、三段版本、比较符、空格 AND、数组 OR、x 通配、~、^ 的保守子集；不支持的范围（含复杂预发布比较）拒绝，不擅自放行。暂不支持 metadata.jars 嵌套模组声明。JAR 内容从不被本站执行。

数据包：上传根目录包含 pack.mcmeta、pack_format=1、字符串描述及至少一个 `data/<ns>/<已知注册表>/*.json` 的 ZIP。需解压内容到 `datapacks/<包名>/` 后 `/reload`，不要直接放 ZIP。拒绝额外外层包装目录、无效 JSON 对象、错误目录、已迁移的关卡旧字段及数据包内的 class/jar/zip。未来新游戏版本应以新迁移登记已核实格式和参考提交，不接受用户自行声明任意兼容版本。

共同检查：不解压至文件系统；拒绝绝对路径、..、反斜线、驱动器写法、重复或大小写冲突路径、符号链接/特殊文件、加密 ZIP，逐条流式校验 CRC。上限 10000 条、单条展开 64 MiB、总展开 512 MiB；大于 1 MiB 的条目限制压缩比 200。JSON 条目另限 2 MiB。图标仅完整可解码 PNG/JPEG，8 MiB、2048×2048；SVG/HTML 不接受。ClamAV 同时检查嵌套内容，超限/加密/超时不放行。

本站校验不会运行游戏或 Java DFU codecs，因此**不能证明包内所有游戏语义、外部内容引用或作者声明在运行中完全兼容**。审核页面保留包摘要、依赖、兼容声明及安装说明，让人工复核；不把结构 valid 等同于运行测试通过。游戏仓库单测和游戏执行不属于本站上传检查链路。
