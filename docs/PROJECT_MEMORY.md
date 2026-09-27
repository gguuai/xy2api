# XY2API 仓库记忆与开发日志

> 这是跨 Agent、跨会话的持续交接文件。开始仓库任务前必须完整阅读；执行过程中在关键节点更新；结束前必须写明结果、验证、卡点和下一步。项目事实变化时，应同步更新本文件，不能只追加日志而保留过期的顶部状态。

## 当前交接状态

### 糖果检测当前健康与长响应兼容（2026-09-27，本地实现）

- 原始副本 `/xy2/xy2api-original` 保持 `1cf9708e73c8c189968016852b07082e1c30e187`；修改在 `/xy2/xy2api`、`fix/iq-current-health`，未推送、未发版、未部署。
- 按用户决定只用糖果题21；当前状态每次尝试更新，错误即未知并避让业务流量，历史有效状态单独保留。正常1次，可恢复异常最多共3次，答错不补试；余额不足至少15分钟自动复检。
- 对指定生产请求的只读查询确认：旧解析器累计读取262145字节触及256 KiB限制。响应是HTTP200/SSE，不能据此断言实际答案错误或正确；原始回答未保存。新解析器 v5 总读取8 MiB、单事件2 MiB、答案64 KiB分开限制，保留完整终态和冲突校验；增加错误分类和限制诊断。
- Go IQ/质量路由单元、隔离PostgreSQL/Redis集成、核心race、迁移校验及服务构建通过；前端27项回归、类型、定向ESLint及生产构建通过。首次集成旧断言失败已修复，失败证据保留。补充socket超时测试结果见账本最新事件。
- 同输入基线错误后仍smart且可调度、长流21/29均unknown；修改后错误为unknown并阻断，长流分别smart/degraded。完整三态、补丁重建、回滚哈希以固定账本 `TRANSACTION_VERIFIED` 事件为准。
- 四角色固定为 `/xy2/artifacts/iq-candy-20260927/{MODIFIED_FILE.tar,DIFF_FILE.patch,VERIFICATION.txt,ROLLBACK.sh}`，源码回滚依赖兄弟 `BASELINE.tar`；不操作生产或数据库。操作说明 `docs/IQ_CURRENT_HEALTH.md`、`docs/OPENAI_IQ_CHECK.md`。后续部署需统一旧工作者状态语义并备份数据库；本轮未执行生产上线。


### Sub2API v0.2.8 / XY2API 0.1.9 已发布（2026-09-23，生产未部署）

- 用户授权使用本机 GitHub 票据完成标准同步、合并、RC 与正式发布。起点 `8a99582ab`；同步 PR #62 在固定 head `63390ddd5` 的 18/18 检查通过后合并为 `522494784915bdc5e00c1a87bd26c5b2e0ea45c9`；正式晋级 PR #63 在固定 head `48a29a65e` 的 18/18 检查通过后合并为 `504f633ee5dfae6d21b541b276cb15da3dcbce3e`。
- 官方标签固定 d7a82d78ca51d42be41cb4daa3510ea401defe9f / fd80b08c90b55edcad5b00171b53f08721d30da1。真实 merge 完成，43 项冲突逐项裁决，来源审计通过；299 个已发布 SQL 字节与 checksum 不变，新增 253–255，总计302。
- 保留 STATE/IQ/质量路由/后台导出/分组长上下文策略，接入上游模型、用量、推理倍率、网关与插件协议更新。工具测试、Compose、生成零差异、前端 348 文件/2629 用例、生产构建、远端完整 Unit/Integration 与安全扫描通过。修复同步后 adminSettings 的付款配置读取失败重试；早期失败及取消记录保留，取消不能视为产品失败。
- annotated RC 标签 `0021a9afa23e7e8e6ce393bc96a003cab5266774` 指向同步合并提交；RC Release run `35889830544` 成功，五平台包 SHA-256、Linux 产品/兼容版本/提交、GHCR 两架构版本及 revision 通过。RC digest `sha256:8238393e465a7f0a8273a59f2e5e3ff9fc9bbcf38f814f74e554b683eb2e318b`；RC 后 latest/0.1/0 保持旧正式镜像。
- RC 全新安装 health/login 200、302 migrations；隔离 0.1.8→RC 迁移 299→302，推理定价旧字段转换，数据库/Redis/应用标记保持；恢复预升级 PostgreSQL dump 后旧版 health/login 200、299 migrations、旧定价字段与全部标记恢复。首轮全新安装夹具缺少数据库就绪等待，补齐后通过。没有生产部署或真实模型调用。
- 正式 annotated `v0.1.9` 标签对象 `7092ad4913b0eab17228677fb472ad3c49e1db38` 指向正式合并提交，Release run `35895236920` 成功；正式版本仅修改 VERSION 与 provenance 产品版本。四角色固定为 `/xy/artifacts/upstream-sync-v0.2.8/{MODIFIED_FILE.tar,DIFF_FILE.patch,VERIFICATION.txt,ROLLBACK.sh}`；源码三态为 0.1.8/0.2.6/299 → 0.1.9/0.2.8/302 → 基线，补丁重建、回滚哈希、重新应用均通过。

- 正式 [Release v0.1.9](https://github.com/liulixin-lex/xy2api/releases/tag/v0.1.9) 为非草稿、非预发布；五平台发布包 SHA-256、Linux 产品/兼容版本与完整提交通过。GHCR linux/amd64、linux/arm64 的 version/revision 均与源码一致，digest `sha256:4d58fdc7dd8a6b09b2b935cef9839e2201fa768d669b39603f40bc7a4076dfa5`；0.1.9/latest/0.1/0 一致。正式镜像独立全新安装 health/login 200、302 migrations。
- 本次 sync/release 短期分支已删除，命名空间上游标签、RC 和正式标签保留。隔离演练资源已清理；源码副本与固定四角色保留。文档收尾前已验证 main=origin/main=正式标签；收尾后 main 仅新增本记忆文档，不移动正式标签。

### 用量导出与管理员指标 0.1.8 已发布（2026-09-22，生产未部署）

- 用户授权合并 PR #58 后台导出与 #60 管理员指标并发版。#58 合并为 `b5dd95473`；组合候选 `1e0f6b2eb` 通过 16/16 检查后，#60 合并为 `5b475476e6fa552b9f5952c76491013aab867d7b`。保护规则保持，原 `/xy/xy2api` 工作文件未修改。
- 正式 [Release v0.1.8](https://github.com/liulixin-lex/xy2api/releases/tag/v0.1.8) 为 latest、非草稿、非预发布；annotated tag 固定上述合并提交，Release run `35726558753` 成功。产品 0.1.8、兼容 0.2.6；主线和标签 CI、安全扫描全部通过。
- 五平台发布包已下载并核对 SHA-256，Linux 二进制产品/兼容版本及完整提交一致；GHCR linux/amd64、linux/arm64 及 0.1.8/latest/0.1/0 一致，镜像 digest `sha256:f2bd333ebb4bf8d85fddc80899ea137636600d3272ebd765f75bb6eaff0be9a8`。
- 组合前端 88 项回归、类型检查、生产构建、1280/390 深浅主题浏览器验证通过。自动合并导致用户页测试缺少 UsageTable 导入，已修复并重新通过；首轮契约测试遗漏也已修复，失败记录保留。
- 隔离 0.1.7→0.1.8→0.1.7→0.1.8、正式版全新安装均健康/登录200；数据库、Redis、应用目录标记保持。迁移由298增至299，新增导出迁移252；旧版回切不删表。管理员显示开关默认true、公共设置无新增字段已实测；初次合成管理员被初始化门禁423拦截，补齐测试账号初始化后通过，未改产品门禁。测试容器、卷及网络已清理。
- 后台导出默认关闭，按 docs/USAGE_EXPORTS.md 完成部署环境负载/容量验收后灰度；管理员两个显示开关默认开启。本轮仅仓库及开发机隔离验收，没有生产部署或真实上游调用。
- 四角色继续位于 `/xy/artifacts/admin-usage-metrics/`；发布结果、字面命令、源码三态、补丁重建/回滚哈希及最终重开记录见 VERIFICATION.txt、ARTIFACTS.json 和 release-0.1.8/。指标三态无/75.0%与50.0T/s/无；6100条导出三态为61次分页失败/0次分页与1个任务/恢复基线。标签不移动，本收尾仅更新项目记忆。

### 管理员使用记录指标（2026-09-22）

- 基于 `main / 021c0d885382ecfb956674e2f8b59a7674e8d912` 创建 `feat/admin-usage-metrics`，实现副本 `/xy/artifacts/admin-usage-metrics/work`；原 `/xy/xy2api` 的 main 源码保持干净。已提交并推送至远端 PR [#60](https://github.com/liulixin-lex/xy2api/pull/60)，功能提交 `017da5ac9`；已合入最新 main `f4cf08f5f`，仅项目记忆文档发生冲突并保留双方记录。最终分支头、合并状态和 CI 结果见固定验证账本。
- Token 列仅显示单次请求百分比数字（一位小数），不显示“缓存命中率”标签；分母包含未缓存输入、缓存创建及缓存读取，请求独立，不跨用户或会话累计。延迟列显示 `速度 50.0 T/s` 等数值，不带“～”；无首字时按总耗时估算，无效数据为 `—`。不改变计费、采集或使用记录 API，不增加数据库迁移。
- 算法复核：OpenAI/Gemini 已将缓存从普通输入扣除，分母无重复计算，速度将毫秒换算为秒。日志中的强制缓存计费会改写缓存读数，不能据此还原真实上游命中；输出可能含思考 Token，首字统计模式也影响速度，故仅为请求日志口径估算。中英文悬浮提示及计算代码注释已说明这些限制。
- `admin_usage_cache_hit_rate_enabled`、`admin_usage_token_speed_enabled` 两个系统开关默认开启，位于功能开关页，通过管理员设置读取/保存，省略更新字段保留原值；公共设置与 SSR 注入不新增字段。共用表格两个显示属性默认关闭，普通用户页不启用；保存成功即时同步 store，迟到读取不能覆盖刚保存的结果。
- 最终 7 个文件共 137 项前端回归、i18n 完整性、定向 ESLint、vue-tsc 通过；Go 管理员设置/DTO/服务层相关回归与默认 vet 通过。生产构建将 vue-tsc 与 Vite 打包串行执行，保留原 Vite 配置并仅移除重复并行 checker。最终构建与 1280/390 深浅主题浏览器复验以固定 VERIFICATION.txt 中 `FOLLOWUP_BUILD_SERIAL_FINAL`、`FOLLOWUP_BROWSER_FINAL` 事件为准。
- 相同输入 BASELINE 无新增指标，MODIFIED `75.0% / 50.0 T/s` 且两个开关四种组合生效，ROLLBACK 无新增指标，均 exit0。4,152 个基线文件与恢复源码哈希一致；4,156 个修改版文件由补丁完整重建，并验证回滚后重新应用补丁。仅源码副本回滚，不操作数据库。
- 四角色固定为 `/xy/artifacts/admin-usage-metrics/{MODIFIED_FILE.tar.gz,DIFF_FILE.patch,VERIFICATION.txt,ROLLBACK.sh}`；基线归档为同目录 `BASELINE.tar.gz`，回滚脚本依赖该兄弟归档，最终哈希见 `ARTIFACTS.json`。命令、stdout/stderr、退出状态和修复后结果保留；内存中断、模板插入错误、测试桩与模拟响应纠错未作为成功。临时 768 MiB swap 已关闭并清理。
- 浏览器使用本地合成请求和模拟 API，验证显示、保存、刷新保持及普通用户无管理员设置请求，不代表真实上游模型性能测量。PR #60 首轮完整 CI 的两个 API 契约测试因预期 JSON 漏掉新增管理员开关失败；现已补齐，保留严格响应比对，未改产品逻辑。修复后完整 CI 状态以固定账本和 GitHub 最新 head 为准。本轮不合并 PR、发版或部署。
### 用量记录后台导出（2026-09-22）

- 以 `021c0d885382ecfb956674e2f8b59a7674e8d912` 为基线，修改在 `/xy/artifacts/usage-export/work`、本地分支 `feat/usage-export`；原 `/xy/xy2api` 的源文件保持不变。2026-09-22 用户追加授权提交、推送和创建 PR；不合并、不发版、不部署。
- 用户 CSV 与管理员 XLSX 已替换为 PostgreSQL 持久任务，独立预算、快照游标、磁盘暂存、流式生成、本地/S3 存储、单次会话下载凭证、取消/过期/清理及可见页面轮询。新增迁移252及校验和，配置默认关闭，并支持用户名单灰度。维护说明 `docs/USAGE_EXPORTS.md`。
- 真实隔离 PostgreSQL/MinIO、HTTP权限与单次下载、同快照并发修改、丢失数据库锁连接、取消及重试、迁移校验、任务引擎race均通过。前端31项定向测试、ESLint、完整类型检查和生产构建通过；后端最终构建及全范围变更静态检查通过（0 issues）；浏览器390/1280任务界面、刷新恢复、原生下载实测通过，导出期间历史分页请求0。
- 合成0/1/100/6100/10万/100万CSV通过，100万零一条明确拒绝。百万CSV约27.8秒、RSS增量6.2MB；百万XLSX约137.7秒、RSS增量54.3MB。Excelize最初整包缓冲超内存门槛，改ZIP直接写文件后重测通过，原失败保留。
- 百万条实际usage_logs结构的SQL负载测试，单/双任务各50万条导出通过；共享开发机缓存及其他编译影响基线，结果不能作为生产HTTP P95承诺。部署等效的完整HTTP负载、硬磁盘配额及生产灰度仍属上线门禁，默认不启用。
- 四角色固定在 `/xy/artifacts/usage-export/{MODIFIED_FILE.tar,DIFF_FILE.patch,VERIFICATION.txt,ROLLBACK.sh}`。同6100条输入：BASELINE请求61页遇429；MODIFIED历史请求0、提交1任务；ROLLBACK恢复61页失败。补丁重建一致，回滚归档SHA-256与原基线一致。最终源码/构建/静态检查结果和角色哈希以交付目录 `FINAL_RESULT.json` 与字面账本为准。


### IQ API 会话隔离 0.1.7 已发布（2026-09-22，生产未部署）

- 基线 `be451c280`，继续在 `/xy/artifacts/gpt-quality-routing/work` 的 `fix/iq-probe-session-isolation` 实施，原 `/xy/xy2api` 的 `021c0d885` 工作文件保持。IQ 独立探测此前不经过业务会话质量路由；现在每个 API Key 实际检测及持久化补试在最终模型映射、请求头覆写之后生成新会话与 `prompt_cache_key`，清除旧亲和、续接和大小写变体幂等头，保留指定账号、题目、判分、次数和禁止 POST 重放约束。
- OAuth IQ 不执行 API 随机会话轮换；新调用已有指纹收敛函数，off/device/session/full 按配置工作，设备+会话及完全收敛保持账号稳定 session/thread，逐轮 turn ID 仍遵循原逻辑。正常业务缓存与粘性未改变；IQ 检测放弃跨尝试缓存亲和，第三方是否换隐藏账号由上游实现决定。
- GPT-6 已在 0.1.6 覆盖，本轮补齐 `gpt-6` 别名、Astra effort/日期变体、正常模型与未知名称回归，文档明确 GPT-5.6、GPT-6 及更新 GPT 文本模型的已登记降档规则。0.1.6 Release 仅更正说明，标签、发布日期及六个附件 ID/大小/校验值回读一致。
- [PR #56](https://github.com/liulixin-lex/xy2api/pull/56) 最终 head `35b67f4402f2e2988d2640ebea36cbb705dabd8b` 在 16/16 检查成功后普通合并为 `934272ed828b05b4c558ae67837e46d67a86c520`；分支保护保持。[Release v0.1.7](https://github.com/liulixin-lex/xy2api/releases/tag/v0.1.7) 为正式 latest，annotated tag 固定 `934272ed828b05b4c558ae67837e46d67a86c520`；Release run `35696263868` 成功。五平台包下载校验、Linux 产品 0.1.7/兼容 0.2.6/完整 commit、GHCR 双架构与稳定别名通过，digest `sha256:f7d19c1ad818e50019058560b4d16ce8bc95178ef00716f7b8559075b863902c`。
- IQ、质量路由、指纹、STATE 定向回归与 race、七项静态规则通过；早期测试夹具误用头大小写/轮次字段、CI 静态写法、共享开发机内存不足导致的编译终止，以及工作树未提交时来源审计拒绝均保留原始失败记录，修正后门禁通过。没有真实模型请求。
- 同一合成号池输入中 BASELINE 三次共用一个隐式会话，结果 degraded/degraded/degraded；MODIFIED 三次使用三个新会话与缓存键，结果 degraded/smart/degraded；ROLLBACK 恢复基线。三态各三次上游调用，均 exit 0。这是可控模拟号池验证，不是生产抽样保证。累计业务质量探针仍为 first/next/stable：1/1/1 → 1/2/2 → 1/1/1。
- 开发机隔离 0.1.6→0.1.7→0.1.6→0.1.7 健康、管理员登录及 PostgreSQL/Redis/应用目录标记通过，全新安装通过，迁移数保持 298，专用测试容器已清理。未连接或操作生产。固定四角色仍在 `/xy/artifacts/gpt-quality-routing/`；累计补丁重建、源码回滚和恢复哈希由 `DELIVERY_RESULT.json` 与 `iq-session-isolation/FINAL_RESULT.json` 记录，源码回滚恢复原始 `021c0d885`，不操作数据库。

### GPT-5.6+ 会话质量路由 0.1.6 已发布（2026-09-22，生产未部署）

- 已执行 `git pull --ff-only origin main`，基线为 `021c0d885382ecfb956674e2f8b59a7674e8d912`；实现位于 `/xy/artifacts/gpt-quality-routing/work` 的 `feat/gpt-quality-routing`，原 `/xy/xy2api` 保持干净。没有部署、生产写入或真实模型调用。
- 新增 `gateway.openai_quality_routing`（默认 enforce，避让 300 秒），以 API Key、分组、稳定会话和模型为作用域；严格比较最终出站模型与原始响应声明。默认 Astra/Sol/Terra→Luna，账号映射 Sol→Terra 不触发。正常缓存标识不变；异常优先不同上游，必要时稳定轮换出站标识。
- 默认/高级/加权调度、HTTP/SSE、Chat/Messages 转换、WS 原生/桥接/透传已接入；当前响应不重放，不新增模型调用，不写全账号质量降权。完整历史才允许迁移续接，缺少 response.output、工具上下文或存在不透明 compaction/reference 项时保留所有者。异常代次内标识稳定，状态过期后再次异常生成新标识。
- 用户确认本次聚焦 API 上游号池：新增质量证据、出站标识轮换及 WS 连接代次只对 OpenAI API Key 类型凭据生效，OAuth 原有指纹逻辑不变；不承诺解黏能改善单个 OAuth 账号的质量。只有一个 API 上游时复用稳定的新出站会话标识，实际是否更换隐藏池账号由上游决定。
- 功能/版本 [PR #54](https://github.com/liulixin-lex/xy2api/pull/54) 最终 head `b4b6a26902715ddc56ae1de3575f4bf465f70018` 在 16/16 检查通过后普通合并为 `f0127cc29db7ee5ba2f54849ff14fdfec064bbf0`，分支保护保持。首轮 CI 的两项旧指标快照测试暴露空 map 与零值兼容差异，修复后完整单元、集成及静态检查全部成功。
- 正式 [Release v0.1.6](https://github.com/liulixin-lex/xy2api/releases/tag/v0.1.6) 为 latest、非草稿、非预发布；annotated tag object `a15aeb9995280994c238ee8863900bb7db285b11` 固定于上述合并提交。Release run `35682383562`、主线和标签 CI/安全扫描全部成功。五平台包实际下载并复算 SHA-256，Linux 二进制产品版本 0.1.6、兼容版本 0.2.6、完整提交正确；GHCR amd64/arm64 及 `0.1.6/latest/0.1/0` 别名均核对一致，digest `sha256:273feaa297e31ecd6247cdf36504a0a6f920c0e44d4c07619b02abb55f56baa8`。
- 定向与扩展回归、service/repository race、七项 Go 静态检查通过；最终候选的质量路由与原调度组合回归/race 均通过，真实隔离 Redis 双客户端竞争、旧代次绑定拒绝、作用域和 TTL 再次验证通过。最终五轮健康请求 p95 增量为 295/1224/-176/1142/315 微秒，最大 1.224ms，低于 2ms 目标；3030 次请求对应 3030 次上游调用，正常正文/缓存标识逐字节不变。异常模拟缓存为 0/800/0/800，轮换后一次冷启动。仅隔离负载结果，不代表生产缓存率或首字保证。
- 开发机专用隔离环境实际完成 0.1.5→0.1.6→0.1.5 回切及再次前滚，健康和管理员登录均 200，PostgreSQL/Redis/应用目录标记保留；0.1.6 全新安装也通过。迁移数始终 298，本版无新增数据库迁移。未连接或修改生产服务，没有真实模型调用。
- 同输入基线探针为 first=1、next=1、stable=1，修改版为 first=1、next=2、stable=2。固定四角色位于 `/xy/artifacts/gpt-quality-routing/`，`VERIFICATION.txt` 保存命令、输入、字面输出和退出码；源码回滚、恢复哈希及补丁重建的最终执行结果由同目录 `DELIVERY_RESULT.json` 记录。源码回滚不操作生产数据库。

### STATE 可靠性 0.1.5 已发布（2026-09-21）

- 用户授权推送、合并和 GitHub 发版，明确主站线上服务不允许操作；本机是开发机，仅使用专用隔离容器验证。整个发布过程没有连接、停止、重启或修改主站服务，也没有真实上游账号调用。
- 功能与版本 PR [#52](https://github.com/liulixin-lex/xy2api/pull/52) 的最终 head `11ba75164f177e8e4e7049a26259bd74970f1d97` 在 16/16 检查成功后普通 merge 为 `f0e60e852be01e02bac3917307ec888f6820c7d1`，分支保护保持。annotated `v0.1.5` tag object 为 `9c437392599f0f6388ea8b66c3637b66c7d0bd8f`，发布提交不再移动；产品 0.1.5 / Sub2API 兼容 0.2.6。
- 正式 [Release v0.1.5](https://github.com/liulixin-lex/xy2api/releases/tag/v0.1.5) 为 latest、非草稿、非预发布，Release run `35571354176` 成功；主线与标签 CI、安全检查全部成功。五个平台包实际下载并复算 SHA-256，Linux 二进制版本/兼容/完整提交正确。GHCR amd64/arm64 的 OCI 标签以及 `0.1.5/latest/0.1/0` 别名均核对一致，digest `sha256:616b68f955aece616d28593ba1211da8440bb2874915773c8dcd3df7e5c5867a`。
- 发布前独立审查修复代理池临时密钥持久化缺陷：非空池的迁移、读取、保存及旧接口替换要求显式配置共享持久 TOTP_ENCRYPTION_KEY；拒绝操作不写密文、不创建权威池、不删除旧配置，空池仍可用。四组新增回归和已有代理/轮换兼容回归通过；测试的一处 staticcheck 写法已修正后通过最终门禁。
- 开发机隔离 0.1.4 升级到正式 0.1.5，健康和管理员登录均 200，迁移 297→298，PostgreSQL/Redis/应用目录标记保留；两条代理密文、脱敏、409 冲突与同密钥重启解密通过；回切 0.1.4 在 298 条迁移上仍可启动、登录并保留标记。再次前滚后代理池及三类标记完整；正式版全新安装健康/登录200、298条迁移通过，专用新装环境已清理。完整执行结果见 `release-0.1.5/` 日志。该验证不代表旧程序可管理新代理池。
- 原四角色继续位于 `/xy/artifacts/codex-state-upgrade/`，此前版本已备份；本次同输入版本为 BASELINE 0.1.4、MODIFIED 0.1.5、ROLLBACK 0.1.4，均兼容 0.2.6、exit0。回滚恢复 4,131 文件，补丁重建 4,152 文件一致；此前手动软等待/无效草稿关闭三态 false/false→true/true→false/false 证据保留。源码回滚不操作数据库。
- `/xy/xy2api` 的 main 已快进到正式合并源码；本次收尾文档由独立受保护 PR 固化，发布标签不移动。最终本地源码/远端提交与四角色哈希见 `release-0.1.5/FINAL_RESULT.json`。成功率策略及质量隔离默认关闭，真实账号覆盖率与质量目标仍待灰度，不能将源码和发布验收当作生产效果证明。

### STATE 可靠性升级与本地源码提交（2026-09-20）

- 基线为 `1a4fd39c71eadc4d5d587a39373bcded692e4fa5` / 0.1.4；源码位于 `/xy/artifacts/codex-state-upgrade/reliability-work`，分支 `feat/state-reliability`。用户要求验证后提交源码，并由用户后续推送；本轮不推送、合并、发布、部署或调用真实账号。
- 已实现 STATE 开关独立即时保存与修订冲突保护、持久手动获取任务、多代理加密配置和同出口检测、健康轮换、完整响应验证、候补与版本隔离、共享预算及独立质量观察。前端按钮和提示精简，预算/质量/诊断放入详情；管理员关闭不受打票中状态阻碍。
- 所有新策略默认关闭，逐步启用；质量隔离默认关闭。生产成功率、覆盖率、质量保持和 265 限流目标尚未实测，须执行批准的真实账号观察窗口；本地通过不代表这些目标已达到。
- 后端全量 unit、修复后 STATE 回归、service/parser race（-parallel=1 避免旧 Gin 测试全局变量竞争，保留用例内部并发）、真实 PostgreSQL/Redis STATE 集成与 repository race、全部 7 项 Go lint、Wire 生成、前端类型/i18n/生产构建、嵌入前端后端构建均已通过。前端全量初跑 2326/2327 通过，旧单代理 UI 断言更新后 64 项受影响回归通过，最后 113 项控制/设置/中英文回归通过。1280/390 深浅主题模拟 API 浏览器验证通过。macOS 专用脚本无法用 Linux 的 stat 参数完成，不计为通过。
- 一个并行只读子智能体完成多轮并发、代理、流验证与回滚脚本审查。静态检查发现的错误处理和表达式问题全部修复；race 发现的完成信号早于回调问题已修复，并原子化有界收尾测试计数；资源中断、一次运行中误清缓存造成的导出文件缺失与后续成功复验均保留字面记录，没有抹掉失败。
- 四角色继续使用 `/xy/artifacts/codex-state-upgrade/{MODIFIED_FILE.tar.gz,DIFF_FILE.patch,VERIFICATION.txt,ROLLBACK.sh}`。本次同输入三态、源码哈希、补丁重建、Git 提交和主仓库本地分支导入的最终事实以 `reliability-20260920/FINAL_RESULT.json`、`COMMIT_RESULT.json`、`LOCAL_BRANCH.json` 为准；这些记录在事务执行后生成。旧四角色在 `reliability-20260920/previous/` 留存。
- 原 `/xy/xy2api` 的 main 工作文件保持不变，完成的分支已以本地 Git fetch 导入供后续推送；提交树必须与已验证源码归档一致。源码回滚仅适用于独立副本，不操作生产数据库，不恢复过期或撤销票据。

### STATE / XY2API 0.1.4 已发布（2026-09-19）

- 用户授权推送、合并及发版；功能与版本 PR [#50](https://github.com/liulixin-lex/xy2api/pull/50) 在 16/16 检查通过后正常合入 `f6161d15eb4d7ac5d51f0badc55d9c5d0e8bc931`。annotated `v0.1.4` 指向该提交，兼容版本保持 Sub2API 0.2.6；没有调整保护规则。
- 正式 [v0.1.4 Release](https://github.com/liulixin-lex/xy2api/releases/tag/v0.1.4) 非草稿、非预发布且为 latest；Release 工作流 `35458815394` 成功。五个平台包实际下载并复算 SHA-256；Linux 二进制产品、兼容版本和 commit 均符合；GHCR amd64/arm64 OCI 标签及 `0.1.4/latest/0.1/0` 别名一致，digest `sha256:c6906dcedc9597f87935b78b67b66d55ef163a78af770d0182c729f5a59d4596`。正式镜像隔离全新安装健康与管理员登录通过。
- 四角色继续位于 `/xy/artifacts/codex-state-upgrade/`。发版时重新执行累计与二期 BASELINE/MODIFIED/ROLLBACK：新增账号受管数 2/0/2，未复验票据可用 true/false/true，私有配置导出 true/false/true；旧时间戳、重复头、旧代理 IQ 接受均为 true/false/true，限流请求 8/1/8，冷却 300/1200/300 秒。全部退出 0，源码回滚和补丁重建一致；产品版本 0.1.3/0.1.4/0.1.3。
- 未部署生产或调用真实上游账号；两个续期周期与 24 小时观察仍是实际启用前的独立验收。升级需停止旧采集工作者；回退先关闭 STATE 总开关，源码回滚不操作数据库，详见 `docs/CODEX_STATE.md`。
- 本条文档由独立受保护 PR 固化，发布标签保持不可变；文档最终合并 SHA、主线/标签检查及交付哈希以原目录 `release-0.1.4/` 与 `ARTIFACTS.json` 为准。

### STATE 二次优化本地交接（2026-09-19）

- 同一 `feat/codex-state-upgrade` 副本已提交一期 `2a743f0afcf205e1f440d3f15a8655b412b38af9`、二期后端 `322b61486`、前端 `49f0bd098`。原 `/xy/xy2api` 保持干净 `9c8abed`；未推送、发布、部署或调用真实账号。
- 二期修复内嵌时间／同票不续命、重复 STATE 头、HTTP 200 内的 JSON/SSE 限流与配额错误、固定代理原地编辑后的 IQ 迟到结果。撤销保留不可用私有票据及原期限，重启后取得相同值仍不延长原截止时间。保留合法唯一 312 信号立即撤销策略。
- 扫描改为每页 100 条元数据、批量领取；调度用不含 STATE 的摘要，注入仍查权威记录。诊断增加阶段、白名单原因、期限和业务核验，普通统计约每 30 秒合并写入。
- 界面按用户要求精简：套餐与缺票策略并排；每模型直接显示票据、业务模型和 IQ，时间与统计收进详情。与账号模块分隔线、字号和表单协调；保护草稿、键盘焦点与最后数据，隐藏／关闭停止轮询。
- 最终 7 个受影响后端包 unit、真实 PostgreSQL 18.1／Redis 8.4 集成、service 与 repository race、7 项 Go 静态检查通过。前端全量 308 文件／2321 项测试及后续文案／i18n 回归、ESLint、类型、生产构建、嵌入后端构建通过；1280／390 深浅主题和键盘、轮询、错误反馈通过。
- 1,000／10,000 账号各两模型的隔离 SQL 模式对照：逐账号模式 4,000／40,000 查询、1,000／10,000 行锁；投影 11／101 分页查询、无领取行锁。最终健康账号扫描回归验证无逐账号领取事务或候选读取；代理编辑竞态实测锁等待后取消旧 IQ 结果。数值不是生产吞吐保证。
- 固定四角色仍为 `/xy/artifacts/codex-state-upgrade/{MODIFIED_FILE.tar.gz,DIFF_FILE.patch,VERIFICATION.txt,ROLLBACK.sh}`。一期快照在 `phase1/`；累计与仅二期的同输入 BASELINE／MODIFIED／ROLLBACK、补丁重建、源码哈希、最终提交与四角色重新打开结果，以同目录 `FINAL_RESULT.json` 和 `ARTIFACTS.json` 为完成依据。失败批次与资源处理没有删除或计为成功。
- 维护与来源见 `docs/CODEX_STATE.md` 和 `docs/third-party/codex-state-second-pass-NOTICE.md`。源码回滚不操作数据库；部署前仍须测试账号两个续期周期及 24 小时观察。

### 292 / STATE 本地融合交接（2026-09-19）

- 已批准基线 `6c12e3f`，社区 `ecf3b9a`；原 `/xy/xy2api` 仍为干净 `9c8abed`。代码在 `/xy/artifacts/codex-state-upgrade/work`，未推送、发布、部署或调用真实账号。
- 已融合账号独立 Pro/Team、多模型票据、固定业务代理复验、PostgreSQL 条件租约和私有运行状态、完整响应守护、IQ 等票／恢复复检／有限自愈。承接旧全局配置，新账号默认关闭，旧票复验不延长到期。现有 IQ 判分、分组提示词、compact、插件、计费与 Stripe 逻辑保留。
- 最终后端 7 个受影响包完整 unit、STATE/IQ 定向回归及真实 PostgreSQL/Redis 隔离测试通过；启动前幂等迁移和迁移前后创建账号默认关闭已实测。pnpm 9.15.9 冻结原锁文件后，前端 308 个文件／2319 项回归、ESLint、i18n、类型、生产构建以及嵌入前端的后端构建通过；1280/390 深浅主题的保存、逐模型获取、冷却、轮询和键盘检查通过。全部 7 项 Go 静态检查和 service 的 STATE/IQ/WebSocket race 通过。仓储 race 与四角色三态事务的最终实际结果统一保存在交付目录 VERIFICATION.txt 和 FINAL_RESULT.json，完成标志以该记录为准。
- 固定交付目录 `/xy/artifacts/codex-state-upgrade/`；维护说明 `docs/CODEX_STATE.md`，来源保存在 `docs/third-party/`。真实测试账号两个续期周期及 24 小时观察仍属上线前步骤。

### Sub2API v0.2.6 / XY2API 0.1.3 已发布（2026-09-18）

- 用户授权按项目标准完成上游同步和发版。原 `/xy/xy2api` 保持原始工作树；实现与证据位于 `/xy/artifacts/upstream-sync-v0.2.6-xy2api-0.1.3/`。
- 固定上游正式 annotated v0.2.6，tag object `8f35716ba69976fc0ec950894114d2604bb5454c`，commit `49a39b6dc1abed30fd227611e8af1108bc427610`，unsigned；真实 merge base `86f93c28ee34cc74b629dafb748bd5ac5ca8c5ea`。预检 PR #46 合并 `35d620900`；标准 prepare、受控模块归一化及 10 项裁决后，同步 PR #47 在固定 head `77bd8641a` 的 16/16 检查通过后合入 `ef3493e13022e0a5a5b190d7c8734cea93729d5f`。
- 纳入 60 个上游提交，接入兑换分页、分组用量索引查询、默认关闭的 Codex 票据管理及网关/界面修复。保留 IQ 行锁/配置、分组系统提示词、长上下文计费、Stripe 托管与模式单选；弹窗保留焦点/嵌套行为并支持跨挂载根唯一标题。297 条历史迁移及 checksum 不变，无新增 SQL。
- 本地 2303 项前端测试、lint/类型/生产构建，后端受影响回归、真实 PostgreSQL/Redis 分组用量/兑换/IQ/缓存集成及 Wire 再生成零差异通过。远端完整 unit/integration、lint、跨平台与安全通过。首轮导入排序 lint 和工具/配置错误保留在证据，不计为成功。
- RC `v0.1.3-rc.1` annotated tag object `c809101321031f011c750b4baa9c850f4e4c84f9` 指向同步合并提交。Release 工作流 `35339800187` 成功；五平台包实际下载并复算 checksum，Linux 版本/兼容/commit 和双架构 OCI 一致。稳定别名保持上一正式版。
- RC 全新安装、0.1.2 -> RC 升级、回切 0.1.2 通过；每次健康及管理员登录 200，297 条迁移、数据库/Redis/应用目录标记保持；隔离旧版三类备份已保存。源码三态为 BASELINE `0.1.2/0.2.5`、MODIFIED `0.1.3-rc.1/0.2.6`、ROLLBACK 恢复 BASELINE，exit0 且恢复 SHA-256 一致。
- 正式 PR #48 在 16/16 检查通过后合入 `2ba6600027887abc119c2c7e9d5201b79228fe7c`，annotated v0.1.3 tag object `bf46509902b6e42a7f674e353220e7f97c4cba38`；Release 工作流 `35342300372` 成功，Release 非草稿、非预发布且为 latest。五平台 SHA-256、Linux 版本/兼容/提交、双架构 OCI 与 0.1/0/latest 别名一致，正式镜像全新安装通过。
- 固定四角色：`MODIFIED_FILE.tar.gz`、`DIFF_FILE.patch`、`VERIFICATION.txt`、`ROLLBACK.sh` 位于上述证据目录，最终源码与补丁重建/回滚哈希见 ARTIFACTS.json。MODIFIED 最终为 `0.1.3/0.2.6`，BASELINE/ROLLBACK 为 `0.1.2/0.2.5`。源码回滚不修改数据库。
- 官方 Sub2API 0.2.5 初始化数据库升级 RC 通过，286→302 条迁移（保留额外历史迁移记录），用户登录与三类数据标记保持。本地新增票据生命周期 race 与服务构建通过。隔离测试资源在收尾阶段删除，实际清单由 FINAL_RESULT.json 记录。
- 未部署生产、未调用真实模型或 Stripe 账户。RC 镜像 digest：`sha256:4bedcfe051ce8e679e212a91b97e2e869ba49500c69203fd84304a78febd9a13`。非阻断构建大包提示保留。

### Stripe 模式单选与支付界面改进 0.1.2 已发布（2026-09-18）

- 基线为原仓库 `main / 9c8abed87987b5f4a8f278cdd411fe3ee7273713`，原工作区保持干净。实现位于 `/xy/artifacts/stripe-hosted/experience-work`；功能提交 `25a983458782962bcb9c25041907de317d50d655` 已通过 [PR #44](https://github.com/liulixin-lex/xy2api/pull/44) 合入，发布提交为 `95a9576d5ff49ccb5c934fdad5c19e46e5d23f1b`。收尾文档分支为 `docs/v0.1.2-closeout`。
- `payment_enabled_types` 中 Stripe 改为关闭、站内、托管单选。后端拒绝同时启用两种模式；历史双模式配置读取时优先托管，未选模式不能创建新订单。历史实例、回调、补偿和退款仍按原绑定处理。
- 用户端支付入口及订单记录统一显示 Stripe；管理员设置、订单筛选及统计保留 Stripe 站内／Stripe 托管区分。服务商说明拆分为地址、环境要求和历史配置保护；已完成订单引用的托管配置修改返回 `HOSTED_CONFIG_LOCKED`，不再误报未完成订单。
- 支付方式、快捷金额及提交按钮改为细边框、平面色和明确选中态，保留键盘焦点、禁用和加载状态；金额前缀随币种变化。前端 133 项测试、i18n 完整性、定向 ESLint、类型检查，以及后端带 unit 标签的支付回归和隔离 PostgreSQL 并发／履约／退款测试通过。最终构建及 1280/390 深浅主题浏览器结果以固定验证账本为准。
- 沿用 `/xy/artifacts/stripe-hosted/{MODIFIED_FILE.tar.gz,DIFF_FILE.patch,VERIFICATION.txt,ROLLBACK.sh}`，旧角色保存在 `pre-experience/`。同输入 BASELINE 为 `stripe 托管 / pressed=null`，MODIFIED 为 `Stripe / pressed=true`，ROLLBACK 恢复 BASELINE；三者退出 0，回滚归档与新基线哈希一致，补丁重建逐文件一致。最终制品清单见 `experience/RESULT.json`。
- 正式 [v0.1.2 Release](https://github.com/liulixin-lex/xy2api/releases/tag/v0.1.2) 非草稿、非预发布且为 latest。PR 的 16 项检查、主线及标签 CI／安全扫描、Release 工作流 `35326891540` 全部成功；五个平台包已下载复算 SHA-256。Linux amd64 程序报告产品 0.1.2、兼容 0.2.5 和上述发布提交。
- GHCR amd64／arm64 的 OCI 版本和提交正确；`0.1.2`、`latest`、`0.1`、`0` 均指向 `sha256:12e7fdc6749b806852b9c72272ee4ab1dbb09bd1cf51fea4f087e7de790f359f`。机器核验结果见 `/xy/artifacts/stripe-hosted/experience/release-0.1.2/RELEASE_RESULT.json`，源码四角色最终清单见 `experience/RESULT.json`。
- 本地预览 `http://127.0.0.1:4185/purchase` 使用模拟 API 和支付跳转，不代表真实 Stripe 收款验收。测试数据库容器已停止。本次完成提交、推送、合并和发版，没有生产部署；本版本无新增数据库迁移，历史订单配置保护保留。

### 分组与 Stripe 托管支付 0.1.1 已发布（2026-09-17）

- 用户授权的两个原始提交已确认、推送并保留：分组 `9fd00b81cbd66a5ffdbdb4f316059345e708f5e2`，支付 `cd258bdd9f1b3cc10f9aa7ee38d2837dba352046`。通过受保护 [PR #42](https://github.com/liulixin-lex/xy2api/pull/42) 合入，固定 PR head 为 `d58abdb95371ede7852d56c361370f40230e82e5`，合并提交 `570aa14cfbe7fd86c7d6e995e155c5551e729b74`。
- 产品 `VERSION` 与 `UPSTREAM_BASE.json.xy2api_version` 为 `0.1.1`，Sub2API 兼容版本保持 `0.2.5`。分组迁移为249，支付未发布迁移顺延为250；295条历史迁移及checksum保持。Ent重新生成零差异。
- PR的16项检查全部通过；本地165项前端回归、后端定向回归、PostgreSQL托管支付及分组/缓存集成、同输入源码回滚通过。首次CI的6项lint问题与2处旧契约断言已修复，公共API契约确认管理员提示词不会泄露。
- 正式 [v0.1.1 Release](https://github.com/liulixin-lex/xy2api/releases/tag/v0.1.1) 非草稿、非预发布且为latest；五个平台包实际下载并通过SHA-256复算，Linux amd64二进制报告0.1.1 / compat0.2.5 /上述发布提交。GHCR amd64/arm64 OCI版本与提交一致，`latest`、`0.1`、`0`均指向 `sha256:f176fcfb2b698f8a74fc513fae409c237e94a1fc8c443e50108d83864f0ef02d`。
- 固定四角色继续使用 `/xy/artifacts/group-system-prompts/` 与 `/xy/artifacts/stripe-hosted/` 下的 `MODIFIED_FILE.tar.gz`、`DIFF_FILE.patch`、`VERIFICATION.txt`、`ROLLBACK.sh`；两套归档现在包含组合发布源码，原功能归档保留于各自 `pre-release-0.1.1/`。发布机器结果和最终哈希见 `/xy/artifacts/release-0.1.1/RELEASE_RESULT.json` 与 `ARTIFACTS.json`。
- BASELINE显示专属标识且不支持托管；MODIFIED隐藏标识但保留权限、重复两次签名事件只入账80并COMPLETED；ROLLBACK恢复BASELINE，三态命令全部exit0且恢复归档SHA-256一致。源码回滚不会撤销数据库迁移。
- Stripe真实测试账户、3DS、真实异步支付和退款尚未联调，启用生产支付前仍按 `docs/STRIPE_HOSTED.md` 验收；有未结订单时保留新回调/补偿/退款处理。此次完成GitHub与GHCR发布，没有生产部署。

### 分组专属标识与系统提示词本地交付（2026-09-17）

- 基线为 `main / 41fd8591c25b075e3f95df58d3b46283a3a70b68`；实现位于 `/xy/artifacts/group-system-prompts/work`，原仓库保持不变。新增 `show_exclusive_badge` 与管理员专用 `system_prompt_config`，专属授权不变，模型独立提示词优先于通用提示词，按映射前客户端模型名匹配。
- 已接入标准/简易模式的新建与编辑表单、用户模型广场和渠道展示；HTTP 对话协议、token 计数及 Responses WebSocket 在最终出站阶段前置提示词并保留客户端内容。新增迁移 249、Ent 生成代码、认证投影与版本 26 缓存快照、数据库失效触发器。详见 `docs/GROUP_SYSTEM_PROMPTS.md`。
- 功能验证已通过：后端 domain/service/handler/middleware/routes/repository 定向回归；真实 PostgreSQL 18.1 / Redis 8.4 持久化、认证投影、两个实例的更新/清空失效；59 项前端回归、i18n、类型、lint 和生产构建；1280/390 浏览器表单保存、校验和隐藏标识展示。测试使用本地模拟上游与隔离数据。
- 固定四角色为 `/xy/artifacts/group-system-prompts/{MODIFIED_FILE.tar.gz,DIFF_FILE.patch,VERIFICATION.txt,ROLLBACK.sh}`；回滚依赖同目录 `BASELINE.tar.gz`，只恢复源码归档。最终静态检查、嵌入前端服务构建、BASELINE/MODIFIED/ROLLBACK 对照与补丁重建的字面输出、退出状态及哈希以 `VERIFICATION.txt`、`FINAL_RESULT.json` 为准。首次工具版本不匹配、内存限制中断及测试工厂/模拟接口修正均保留记录，不计为成功。
- 用户后续授权本地提交；在同一源码副本提交已验收功能，提交结果与四角色重新核验记录见交付目录 `COMMIT_RESULT.json`。不推送、发版、部署生产或调用真实模型账号；本机预览使用模拟数据，不能视为生产网关。

### stripe 托管本地功能分支（2026-09-17）

- 基线为 `/xy/xy2api` 的干净 `main` / `41fd8591c25b075e3f95df58d3b46283a3a70b68`。实现位于独立 worktree `/xy/artifacts/stripe-hosted/work`、分支 `feat/payment-stripe-hosted`；用户已追加授权本地提交，提交身份与实际执行结果记录在固定交付目录 `VERIFICATION.txt`。未授权推送、合并、发版或部署。
- 新增独立 `stripe_hosted` Provider 和官方 Checkout 托管跳转，使用已有 `stripe-go/v85 v85.0.0`，API `2026-03-25.dahlia`。覆盖余额充值与套餐单次购买，旧 `stripe` 保留为独立备用入口。
- 新增用户范围幂等请求键、冻结订单报价、实例绑定签名回调、整数金额及账户/模式校验、`PROCESSING`、延迟到账补偿与支付/取消竞争处理。退款先持久化扣回与请求，再向 Stripe 发起；不确定结果保留冻结扣回，确认失败仅恢复一次。追加可空字段迁移 249，历史 SQL 不变。
- 配置密钥加密脱敏，历史实例受订单引用保护；返回地址由可信前端地址生成，托管 URL 仅允许官方 Checkout。用户界面沿用原主题，托管流程不加载 Stripe.js，支付成功提示以本站履约完成为准。配置及回退说明见 `docs/STRIPE_HOSTED.md`。
- 相关 Go 回归、真实 PostgreSQL 场景及托管支付 race 通过；前端 lint、130 项测试、类型检查、生产构建及嵌入前端的后端构建通过，独立迁移兼容检查通过。1280/390 深浅主题支付跳转、到账状态、配置弹窗和键盘焦点通过。最终同输入源码事务的实际命令、输出、退出码与哈希统一以 `/xy/artifacts/stripe-hosted/VERIFICATION.txt` 为准。
- 固定四角色为 `/xy/artifacts/stripe-hosted/MODIFIED_FILE.tar.gz`、`DIFF_FILE.patch`、`VERIFICATION.txt`、`ROLLBACK.sh`。源码回滚不撤销数据库迁移，已有托管订单需保留新版本回调及密钥处理历史付款。
- 真实 Stripe 测试账户未配置，官方托管页、3DS、实际异步支付、CLI/Dashboard 回调和退款仍是上线前门禁。依赖审计记录了既有 xlsx 高危及 x/mod 公告；新支付路径不使用 xlsx，审计不声明全仓无漏洞。详情见交付目录 `SECURITY_REVIEW.txt`。

### Sub2API v0.2.5 / XY2API 0.1.0 同步发布（2026-09-16）

- 用户已授权完整同步、受保护 PR 合并、`0.1.0-rc.1` 隔离验证与正式 `0.1.0` 发版；不包含生产部署或真实模型账号探测。
- 起点为干净且与远端一致的 `main` / `b2c99a3cbcb45abc5aaef5164f8ed92a6c08aad2`，产品 `0.0.13`、兼容基线 `0.2.4`。目标为正式 annotated tag `v0.2.5`，tag object `4af0e80db1b0bc7626dfb8fb76ccaffc6bb0dc17`，commit `86f93c28ee34cc74b629dafb748bd5ac5ca8c5ea`，签名状态 `unsigned`。
- 三方预检发现 16 个冲突，其中 10 个路径原先未登记。预检策略与 gRPC 安全修复已由受保护 [PR #38](https://github.com/liulixin-lex/xy2api/pull/38) 合入 `main`，合并提交 `24e81a9f219b776cd081ae225a976aa1f97b4b43`；gRPC 最终固定到修复 GO-2026-6443 与 GO-2026-6348 的 `v1.83.2`。
- 标准同步分支 `sync/sub2api-v0.2.5` 已通过 `sync.py prepare` 生成：merge 提交 `d58eed562`，模块路径归一化提交 `c648844d7`，初始 provenance 提交 `498941314`。16 项人工冲突已逐项裁决，两条上游 `238_*` 迁移按只追加规则映射为 XY2API `247_*` 与 `248_*`；同步 PR 为 [#39](https://github.com/liulixin-lex/xy2api/pull/39)，产品版本已由独立 PR #40 晋级 `0.1.0`，兼容版本为 `0.2.5`。
- 同步 PR #39 已在固定 head `697418d8a569d6682adc6e225b0766d7c934310e` 的 16/16 检查通过后合并，merge commit 为 `22527fc5cc5f6954c4f496981ee63046860d05f7`；合并后 CI 与安全扫描通过。Go 1.27.0 完整 unit/integration、真实 PostgreSQL/Redis 集成、受影响并发路径 race、后端构建、14 项同步工具测试、295 项迁移 checksum、Ent/Wire 零差异、前端 lint/typecheck/i18n、2215 项 Vitest 和生产构建通过。
- annotated `v0.1.0-rc.1` 指向同步合并提交；Release 工作流 `35058956232` 成功。五平台包 SHA-256、Linux 产品/兼容版本/提交、GHCR amd64/arm64 OCI 标签均已核验；RC 后 `latest`、`0.0`、`0` 仍为旧 digest `sha256:304b160d8223e7c6ec2a3b7b162390b0f40f89a8d87e782b356c82fac2a90445`，`0.1` 仍不存在。
- RC 隔离全新安装与 `0.0.13 -> RC` 升级成功，迁移 293 -> 295；登录、用户、设置、Redis 与应用文件标记保留，248 删除无上限配额且保留有限额记录。实际将 PostgreSQL dump、Redis RDB、应用目录备份恢复到独立目录后，旧版 `0.0.13` 再次健康、登录成功，293 条迁移和被删除配额恢复。正式候选仅将 VERSION 与 provenance 产品版本晋级 `0.1.0`，兼容版本保持 `0.2.5`。
- 官方 `weishaw/sub2api:0.2.4`（commit `5de5e2bed035d43591a2e10e51f420ef6a84eb98`）初始化的独立数据库也已完成 RC 升级：健康与登录成功，用户、设置、Redis、应用目录标记保留，247/248 与配额保留/清理行为通过。该来源保留额外历史迁移文件，记录数为 284 -> 298。
- 正式版本 PR [#40](https://github.com/liulixin-lex/xy2api/pull/40) 在固定 head `f05d9292bdf5f5c4e572da21730fd8e13e482fc8` 的 16/16 检查成功后合并为 `380a9260ea514893ea412f673fcb21618fcab975`；annotated `v0.1.0` 指向该提交，正式 Release 工作流为 `35062247875`。
- 正式 [Release v0.1.0](https://github.com/liulixin-lex/xy2api/releases/tag/v0.1.0) 已公开，非草稿、非预发布且为 latest；发布、main/标签 CI 与安全扫描全部成功。五个平台包实际下载 SHA-256 校验通过，Linux 二进制及 amd64/arm64 OCI 标签均为产品 `0.1.0`、兼容 `0.2.5`、提交 `380a9260ea514893ea412f673fcb21618fcab975`。正式镜像 digest 为 `sha256:f82a63c7d9a806adc714518b80d85cce14cf7094d345a94c02de2ee2807c9046`，`latest`、`0.1`、`0` 一致，`0.0` 保留旧 digest。正式镜像隔离安装健康、登录和 295 条迁移通过。DockerHub 因现有凭据缺失按条件跳过。
- 同输入源码事务已验证：BASELINE `0.0.13 / 0.2.4`，MODIFIED `0.1.0 / 0.2.5`，ROLLBACK 恢复 BASELINE，恢复与补丁重建的文件字节/可执行位/符号链接树哈希一致；完整与简化发布配置的 RC 稳定别名均跳过。四角色及最终源码哈希位于 `/xy/artifacts/upstream-sync-v0.2.5-xy2api-0.1.0/`。宽泛 race 选择器曾包含内存分配上限基准而失败，普通完整单测及准确并发选择器均通过，原失败保留在证据中。

### v0.0.13 发布（2026-09-15）

- 用户明确授权将智商检测简化与普通 HTTP 403 自动续检改动提交、推送远端并发布 0.0.13；授权包含按现有受保护分支流程创建 PR、等待必需检查、合并、创建 annotated `v0.0.13` 标签并核验 Release 制品，不包含生产部署或真实账号探测。
- 功能提交 `1fd4d04a7` 已由受保护 [PR #35](https://github.com/liulixin-lex/xy2api/pull/35) 合入；两个触发来源的 16 个检查运行实例全部成功，合并提交为 `368bc735d3e5b069b96ef0f33032123c9cef7f6b`。产品 `VERSION` 和 `UPSTREAM_BASE.json.xy2api_version` 已晋级为 0.0.13，Sub2API 兼容版本保持 0.2.4。
- `release/0.0.13` 交接文档已由受保护 [PR #36](https://github.com/liulixin-lex/xy2api/pull/36) 合入，最终 main 提交为 `11790409f602c4c48e68091e5d032af450f226a0`。annotated `v0.0.13` 标签的 tag object 为 `a92fc3f99e`，Release 工作流、主线 CI 与安全扫描均成功；Release 已公开、非草稿、非预发布且为 latest。迁移 246 删除共享配额表；回退旧程序须恢复升级前数据库，不能只替换二进制。五平台包、Linux 二进制、GHCR 双架构和稳定别名的实际结果见 `/xy/artifacts/openai-iq-check/v013-release-result.json` 与 `VERIFICATION.txt`。

### v0.0.12发布（2026-09-15）

- 用户补充明确授权最终发布0.0.12，覆盖上一条“暂不发版”，仍不包含生产部署。功能PR #33已在16个检查全部成功后合并为a0aac255d5169f0526da949c49439e33677fce9f，原main已同步且干净。
- 同一实现副本切换到release/0.0.12，只晋级VERSION与UPSTREAM_BASE.json.xy2api_version为0.0.12，兼容版本0.2.4及已验收功能保持。版本提交经过受保护PR后创建annotated标签，由既有Release工作流发布五平台制品及双架构镜像。
- 功能合并、源码回滚与四角色复验结果见reliability-merge-result.json。发布PR、标签、Release、SHA-256及GHCR验证的最终状态见交付目录v012-release-result.json及原VERIFICATION.txt；此记录中的“发布”操作在实际结果产生前不代表完成。

### 稳定性改进提交合并（2026-09-15）

- 用户本轮明确授权提交、推送远端并合并，暂不发版；不创建版本标签或Release，不操作生产。
- 功能提交 `76f6c35a1` 已推送到 `fix/iq-reliability-v2`，正式PR为 [#33](https://github.com/liulixin-lex/xy2api/pull/33)，目标main。源码与已验收归档一致，干净提交上的兼容审计通过。后续提交用于交接或检查修正，最终head、检查与合并提交以PR实时状态及 `/xy/artifacts/openai-iq-check/reliability-merge-result.json` 为准。
- 本轮交付不包含发版；产品版本保留0.0.11。八项必需检查成功后合并，不绕过分支保护。四角色继续使用原绝对路径，源码归档/补丁随最终提交更新，原BASELINE、MODIFIED、ROLLBACK行为及恢复哈希保留。

### 智商检测稳定性实施（2026-09-15）

- 当前基线为远端main/b07824af9（v0.0.11），原仓库已快进拉取且保持干净。实现副本为 `/xy/artifacts/openai-iq-check/reliability-work`，分支 `fix/iq-reliability-v2`；此条覆盖下方历史工作树和发布中描述。
- 本轮按批准方案实现完成消息补全、最多两次持久化尝试、最后有效判定保留、协议故障30/60分钟冷却、三轮尝试明细、30天小时聚合、5秒扫描和新建5/120/576默认值。既有账号周期/预算、原题及标准答案21保持。
- 只实施本地源码、临时数据库/Redis和合成浏览器验证；未执行生产操作、真实检测、提交、推送或发布。部署与24小时真实账号观察仍须后续授权。实施及兼容说明见 `docs/IQ_RELIABILITY_IMPLEMENTATION.md`。
- 固定四角色位于 `/xy/artifacts/openai-iq-check/`，本轮基线及上一版角色备份在 `pre-reliability-v2/`。相关后端测试、真实数据库/Redis集成、31项前端测试、静态检查、类型/i18n、Vite与嵌入前端服务构建已通过。源码事务前后及回滚行为已实测，最终封存结果及全部字面退出记录以 `reliability-delivery.json`、`reliability-final-result.json` 和 `VERIFICATION.txt` 为准。

### v0.0.11发布准备历史（2026-09-15，已由b078基线承接）

- 用户新授权为提交、推送、合并发布所需PR及发布0.0.11；生产操作禁令继续有效。本轮无生产连接。
- 产品VERSION与provenance已调整为0.0.11；Sub2API兼容0.2.4保持。包含PR31后的繁忙/延期、租约/额度、迁移244、记录页面与弹窗交互优化，功能源码与上一轮验收版本一致。
- 发布门禁发现新增244漏登记checksums.json，已追加该条规范化SHA-256，全部旧迁移校验值保持；首次未提交状态的审计exit1保留，提交后复验。
- 此段记录当时ce6082671上的发布准备；当前远端已为b07824af9，版本0.0.11。本轮实施未重新审核发布工作流或制品，不沿用历史“尚不存在”作为当前事实。
- 固定四角色保持/xy/artifacts/openai-iq-check下原路径，发版前归档备份在pre-v011-release，最终事务与制品验证追加同一VERIFICATION.txt。

### 最终审查与减少延期（2026-09-15）

- 用户最终强调尽量减少延期和账号繁忙。当前实现已取消C-1容量预留和三次延期门槛：任何空闲槽位均可直接原子竞争，持续满载之前最多内部等待250/500/1000ms、合计4次获取，不重复发送模型请求。繁忙次数仅供诊断。
- 短健康冷却不再强制放大到一分钟；StartIQCheck复核业务额度，Start/Defer保留所有已知最晚约束；追加244迁移修复批量配置重置时的busy_deferrals清零，与单账号一致。历史迁移311个文件与修改前归档逐字节一致（其中含迁移测试/支持文件，不等于SQL迁移数量）。
- BaseDialog修复唯一标题ID、Tab循环、卸载焦点恢复、嵌套Escape和滚动锁；列表保持32px摘要，记录详情按需加载。98项相关前端测试与后续14项重叠测试、桌面1280/手机390浏览器及最终前端构建通过；账号页784.96kB，详情12.09kB，旧大包提示保留。
- 后端16项检测仓储测试、domain/iqcheck/service相关测试及最新7项服务监测主测试通过。新策略实测最后空位直接执行，短暂竞争两次后约0.75秒启动一次请求，5秒冷却不放大；回滚副本再次复现对应错误，恢复前3944文件字节与修改前归档一致。最终服务层综合静态检查exit0/0 issues，嵌入前端的服务编译于08:38:05 UTC成功；顺序门禁final-audit-check-state.json已达到next=5。
- 期间并行编译造成内存压力，最终构建曾exit137；Go检查主动SIGTERM、首个减少延期回滚命令exit143无完整记录，均未当通过。已改为final-audit-finish-checks.mjs顺序验证。本轮完全没有连接生产；历史生产脚本禁止继续。
- 最终规范见openspec/changes/iq-detection-operations/final-audit.md。继续使用原四角色；完整验收状态与源码哈希以site-review-final-result.json及VERIFICATION.txt为准。

### 本地续作结果（2026-09-15T07:40Z）

- 本轮仅在既有独立worktree继续优化，没有连接、读取或写入生产环境。原工作区main保持PR31合并状态。
- StartIQCheck新增账号行锁内健康冷却复核，覆盖OverloadUntil、RateLimitResetAt与TempUnschedulableUntil，防止领取后健康状态变化仍启动；拒绝启动不扣预算、不增加记录，最长冷却到期后自然恢复。
- 同一数据库竞争测试：BASELINE错误启动，exit1；MODIFIED延期并在到期后启动，exit0；独立副本ROLLBACK重新复现错误启动，exit1。恢复源码SHA-256与修改前相同；失败是预期的缺陷对照，不是修复版本失败。
- 记录弹窗改为defineAsyncComponent并只在打开时挂载。前端14项相关测试、ESLint及完整类型/i18n/构建通过；账号页脚本796.39→784.96kB，详情独立12.09kB。原有大包提示仍存在。
- 检测仓储14项测试及仓储golangci-lint通过（0 issues）。沿用原四角色，累计归档重建、源码回滚及最终清单以site-review-local-continuation-closeout.mjs与VERIFICATION.txt记录为准。没有发布或部署新代码。

### 最新范围纠正与现场结果（2026-09-15T07:23Z）

- 用户明确禁止触碰线上环境：仅允许只读分析，所有优化继续在本地。禁止部署、停止、重启、恢复、备份、清理或修改生产配置；历史脚本与旧授权解释不得触发后续线上执行。
- 经指定跳板机，以ubuntu成功登录生产并sudo只读核查。实际程序0.0.10/e695c0356；容器基础镜像标记0.0.1且程序被覆盖。真实站点gguuai.com与api.aiaimax.com。
- 两次只读采样：217/244/247上限100，占用分别3/4/6与1/2/3，仍account_busy。225因403暂停；246健康冷却。现有本地容量、连续等待、错误分类与精简界面修复符合现场证据，尚未在生产验证。
- 严重执行错误：误把完成优化升级解释为生产部署，创建备份与候选/回滚镜像，06:34 UTC停止原容器并改写Compose；创建新容器及自动回退均因名称冲突失败。用户叫停后不再线上写入。07:23 UTC只读确认原镜像容器running、站点health200，恢复原因未查明，不能声称由本轮恢复。
- 残留变更：/opt/xy2api/backups/iq-20260915、/opt/xy2api/releases/iq-20260915、上传的/tmp程序及本地镜像；Compose曾被自动回退脚本改为回滚镜像，当前配置未再读取。不得擅自清理或恢复。public schema备份成功但不包括无权限访问的legacy_newapi与migration_meta。
- 本地业务源码保持已测试版本。后端综合静态检查最终exit0/0 issues，单测、PG/Redis集成、前端验证与源码回滚证据保留；现场结论和操作事件补入本地文档及四角色交付。

### 检测运行与展示优化（2026-09-15T05:55:00Z）

- PR [#31](https://github.com/liulixin-lex/xy2api/pull/31) 已在八项必需检查通过后合并；PR head `b09351fc18d8c1f7262ea0f1836162f558edcf0f`，merge `ce60826714b06acef5ea24f3aa2bdddc44431390`。`/xy/xy2api` 的 main 已拉取该提交，保持干净。
- 新改进位于独立 worktree `/xy/artifacts/openai-iq-check/site-review-work`，分支 `fix/iq-detection-operations`，尚未提交或部署。繁忙条件从任何占用改为容量判断，三次繁忙等待后允许竞争最后空闲名额，新增持久化 `busy_deferrals`，保留容量、预算和冷却约束。
- 列表简化为开关和评分；记录页独立获取状态、显示延期信息、轮询和排队，诊断下载已在本地 Chromium 实际落盘并解析。此结果补充旧审计 PARTIAL，不代表真实账号验收。
- 最终相关后端单测、真实 PostgreSQL/Redis 集成、前端相关测试、类型/i18n/lint/生产构建、服务编译和独立 go vet 均成功；六输入 BASELINE/MODIFIED/ROLLBACK 通过，累计补丁可重建相同 SHA-256 的归档。综合静态检查首轮 SIGKILL、低内存重试 exit 4 超时，不能把其中 0 issues 当作成功；缓存复验最终结果见 VERIFICATION.txt。保留原有一项 Redis 批量负载集成测试跳过及构建体积提示。
- 用户补正生产用户名为 ubuntu。首次密码认证成功，后台复用连接失效后，后续 SSH 返回 Connection refused，尚未成功读取任何远端命令输出；HTTP IP 仍返回 Caddy 308，缺真实域名/SNI。生产内部只读审计仍受阻。本轮未部署、发版或发出真实模型请求。方案见 `openspec/changes/iq-detection-operations/`。

最后更新：`2026-09-23`（UTC）；下表记录 v0.1.9 发布来源，最终交接文档提交位于发布标签之后。

| 项目 | 当前事实 |
| --- | --- |
| 仓库路径 | `/xy/xy2api` |
| 当前分支 | main；最终交接文档由受保护 PR 合并，发布标签保持不可变 |
| 发布提交 | `v0.1.9` / `504f633ee5dfae6d21b541b276cb15da3dcbce3e`，版本 PR #63 |
| 工作树 | 发布来源已核对干净；本表所在文档只补充验收记录 |
| XY2API 产品版本 | 本地与远端 main 产品 `0.1.9` / 兼容 `0.2.8` |
| 已审计的 Sub2API 基线 | `v0.2.8` / commit `fd80b08c90b55edcad5b00171b53f08721d30da1`；同步 PR #62 |
| 基线 provenance | resolved，43 项人工裁决；同步 merge `522494784` |
| 本地远端 | `origin` 可读写；`upstream` 仅允许 fetch，push URL 为 `DISABLED` |
| 当前环境工具 | git、Python、gh、Docker、Node、pnpm；本轮按仓库固定版本使用 Go 1.27.0 隔离工具链 |

Sub2API 兼容基线已更新到 `v0.2.8`。下方历史日志保留原样；本轮没有升级生产实例。

## 进行中的工作

- `20260927-iq-current-health-pr`：用户已授权提交、推送并创建 PR；复用 fix/iq-current-health 和原四角色，业务实现不变。不合并、不发版、不部署；核对远端与认证后执行发布。

- `20260927-iq-current-health`：本地代码与测试完成；四角色封装和回滚验收结果见固定账本最终事件。生产仍为只读，发布和部署未执行。

- `20260923-sub2api-v0.2.8-sync`：实现、合并、RC、正式发布与隔离验收均完成；本收尾 PR 仅归档项目记忆。

- `20260922-release-0.1.8`：两个 PR 已合并，正式发布及制品/隔离验收完成；文档收尾记录最终结果，发布标签保持不变。

- `20260922-admin-usage-metrics`：PR #60 的两处管理员设置 API 契约预期已补齐新增字段，完整 CI 最终状态由固定验证账本及 GitHub 最新 head 记录。

- `20260921-state-release-0.1.5`：功能/版本 PR #52、正式 Release、五平台与 GHCR 核验已完成；仅剩收尾文档受保护合并及开发机专用测试资源清理，最终事件记录到 release-0.1.5/FINAL_RESULT.json。主站禁止操作。

- `20260920-state-reliability`：实现、代码门禁、同输入源码三态、补丁重建和本地源码提交均已完成；最终 commit/四角色哈希/干净状态/主仓库分支导入见 reliability-20260920 的 FINAL_RESULT.json、COMMIT_RESULT.json、LOCAL_BRANCH.json。无远端推送或生产操作；真实账号灰度仍待执行。






- `20260917-group-prompts-commit`：本地提交交接已登记；实际提交 SHA、干净状态与归档一致性由 `COMMIT_RESULT.json` 记录，无后续远端操作。
- `20260917-stripe-hosted`：本地实现与验收已完成，独立 worktree `/xy/artifacts/stripe-hosted/work`、分支 `feat/payment-stripe-hosted`，基线 `41fd8591c`，原 main 保留。BASELINE 不支持托管、MODIFIED 创建 Session 且重复两次事件只入账 80、ROLLBACK 恢复基线，三者 exit0；回滚字节及补丁重建均一致。最终四角色哈希见 `/xy/artifacts/stripe-hosted/VERIFICATION.txt`。真实测试账号联调另行完成，不推送或部署。

- `20260916-sub2api-v0.2.5-xy2api-v0.1.0`：同步、RC、正式发版与全部制品/隔离验收已完成；本条最终文档通过受保护 PR 固化后执行本地 main 同步、最终归档和专用测试资源清理。固定证据目录为 `/xy/artifacts/upstream-sync-v0.2.5-xy2api-0.1.0/`，以 `FINAL_RESULT.json` 记录收尾提交与四角色哈希。无生产部署或真实账号探测。

- `20260915-v0.0.13-release`：功能 PR #35 与交接 PR #36 均在 16/16 检查成功后合并；`v0.0.13` Release、五平台包、Linux 二进制和 GHCR 双架构已核验。仅剩本条最终状态文档通过受保护 PR 固化；生产部署不在范围内。

- `20260915-iq-simplification`：本地实现已完成，副本 simplify-work/fix/iq-simplification，基线63f0a036/v0.0.12。删除日预算、节约模式及共享配额，普通403周期续检，新增迁移246并简化中英文设置和诊断。相关单元、PostgreSQL/Redis集成、导入导出、84项前端测试、前端lint、类型检查、生产构建、嵌入前端的后端构建及1280/390浅深色浏览器验收通过；最终后端静态检查结果与四角色哈希以交付目录 simplify-final-result.json 和 VERIFICATION.txt 为准。相同输入的403/旧预算 BASELINE、MODIFIED、ROLLBACK及补丁重建已验证，完整3951文件基线哈希一致。原仓库未修改，生产仅只读；迁移246删共享配额表，回退旧程序需要恢复升级前数据库，源码回滚脚本仅处理本地副本。

- `20260915-v0.0.12-release`：用户新增授权发布0.0.12，功能PR33已合并；正在版本提交、受保护合并和发布制品核验。无生产操作。

- `20260915-iq-reliability-merge`：功能已提交并推送，受保护合并与最终检查由PR #33跟踪，具体结果写入外部 `reliability-merge-result.json` 及原VERIFICATION.txt；用户明确暂不发版。交接完成后按PR状态重新核实，不能据本文件历史“未提交”描述重复提交功能。

- `20260915-iq-reliability-v2`：本地实施与验证完成；本轮基线b07824af9，独立副本reliability-work。终态补全、两次尝试、有效判定、统计及新建默认值已实现，存量配置与原题保留。生产观察尚未执行，后续部署另行授权；四角色及交接文档已包含验收与回滚证据。

- `20260915-v0.0.11-release`：用户已明确授权提交、推送和发布v0.0.11，正在核验版本、受保护PR与发布制品。对象保持原独立worktree；生产部署、重启和其他线上操作仍禁止。

- `20260915-iq-final-audit`：本地代码修复及验收已完成，最终服务层静态检查与嵌入前端编译均通过。原四角色备份与源码哈希在pre-final-audit；交付归档重建、四角色重读与最终哈希由final-audit-closeout.mjs和final-audit-finalize.mjs记录到site-review-final-result.json，checkpoint只允许本地续作，禁止生产操作。

- `20260915-iq-local-continuation`：已完成。本地实现、相关测试、冷却BASELINE/MODIFIED/ROLLBACK、累计补丁重建归档和恢复源码哈希一致性均通过；原四角色已更新，最终结果见site-review-final-result.json。生产操作继续禁止。

- `20260915-iq-production-upgrade`：已停止且禁止继续。误执行部署失败及原容器停止事件见顶部记录；07:23只读确认原容器运行、health200。用户最终范围为只读生产、本地优化，不执行历史部署或恢复脚本。

- `20260915-iq-site-review`：本地实现与应用验证、六输入源码事务已完成；生产用户名已补正为 ubuntu，当前 SSH 端口拒绝连接且缺站点域名，待连接恢复后继续只读核查。沿用 `/xy/artifacts/openai-iq-check/` 四角色与最初基线，旧角色备份到 pre-site-review。

- `20260915-v0.0.10-audit-pr`：修复已提交正式PR #31，gguuai:fix/v0.0.10-audit → liulixin-lex:main；本地源码/测试哈希与提交审计通过。首次远端CI及Security Scan为action_required，等待维护者批准运行；未合并、发版或部署。

- `20260914-v0.0.10-release`：功能PR #29、主线CI、安全扫描、Release及五平台包/双架构镜像均已通过；发布完成。本条记录随收尾文档合入，实际PR状态以GitHub为准。

- `20260914-iq-ui-refinement`：已由v0.0.10发布承接；UI和三轮保留已提交发布，未部署生产。

- `20260914-iq-monitoring-implementation`：已由v0.0.10发布承接；历史本地验证证据保留，真实账号观察尚未执行。

- `20260914-iq-monitoring-plan`：已由本轮实施承接；原文档验证保持历史记录，当前实现与验收进度以monitoring/tasks.md及monitor-impl-*记录为准。

- `20260914-iq-protocol` 已随v0.0.10提交发布，无真实上游调用或生产部署。

- `20260914-iq-stability-implementation` 的后续发布已在 v0.0.9 日志记录；历史未提交描述不代表当前状态。

- `20260914-iq-stability-plan` 已由本轮实施承接；完整规格与任务证据在 `openspec/changes/stabilize-openai-iq-check/`。

- `v0.0.8` 功能、提交、发布和制品验收已完成；本次文档提交记录收尾状态。原始 v0.0.8 功能无遗留实施事项；新增改进需求见上一条。四项交付文件及发布日志继续保留于 `/xy/artifacts/openai-iq-check/`。

## 当前重要事项

### OpenAI 智商检测与 XY2API v0.0.8 已完成

- 功能和版本通过 [PR #25](https://github.com/liulixin-lex/xy2api/pull/25) 合入；最终 head `307679ee82407a04c6f4055d8ce68c6d4151b3f5` 的 16/16 检查通过，merge commit 为 `accb4ad7656a2ca3d2eee3df601e38f4e5b02157`。
- 正式 annotated tag `v0.0.8` 的 tag object 为 `3c646ffd000bfdc052183bd2ec94b9b2d3cfea56`。Release run [34803057678](https://github.com/liulixin-lex/xy2api/actions/runs/34803057678) 成功，Release 非 draft、非 prerelease 且为 latest；合并后的主线 CI `34803053670` 与安全扫描 `34803053686` 均成功。
- 5 个平台安装包均已实际下载并通过 SHA-256 复算；Linux amd64 二进制 `-version` 显示产品 `0.0.8`、兼容 `0.2.4` 和正确发布提交，退出码为 0。
- GHCR `0.0.8`、`latest`、`0.0`、`0` 均指向 `sha256:b1aa56e04248f496c8c2bce7230d99d924ac6e13dabac88583f73c479c46237a`，包含 linux/amd64 与 linux/arm64，OCI version/revision 正确。
- 功能默认关闭；判题接受整数 answer JSON 和明确结论为21的解释，异常为未知。追加 migration 240，历史迁移校验不变。使用说明见 `docs/OPENAI_IQ_CHECK.md`。
- 发布门禁发现并修复原有清理测试参数、SQL mock 新字段/锁查询，以及新增集成测试遗留 scheduler_outbox 的隔离问题。最终完整单测、集成测试和静态检查通过；本机完整仓储集成包也通过。无真实账号调用或生产部署。

### Sub2API v0.2.4 与 XY2API v0.0.7 已完成

以下状态于 `2026-09-11T13:09Z` 通过本地 Git、GitHub Actions、Release、GHCR 和隔离 Docker 环境核实：

- 预检 PR [#21](https://github.com/liulixin-lex/xy2api/pull/21) 登记 3 个新冲突路径；同步 PR [#22](https://github.com/liulixin-lex/xy2api/pull/22) 的最终 head `b3d6b4d21ae042cdba016341809fef6775a1409d` 在 16/16 检查通过后，以 merge commit `ea48f08fc8d436d43dceacdc9728c3928e95cd0d` 合入。上游 annotated tag object 为 `d681d0798064ee0ffff376d19687d12f09fe600f`，目标 commit 为 `5de5e2bed035d43591a2e10e51f420ef6a84eb98`，签名状态为 `unsigned`。
- 本次纳入 70 个上游提交并裁决 7 个冲突；新增 MiniMax、HTTP/2 keepalive、Grok 媒体资格、Image 2.5、OpenAI 周成本聚合、监控排名及上游稳定性修正。完整矩阵见 `docs/upstream-sync/v0.2.4.json`。
- 上游迁移 237 与 XY2API 已发布的 237/238 编号冲突，已只追加为 `239_add_minimax_platform.sql`；历史 285 个 migration checksum 全部不变，新 checksum 为 `f4c73d2dbce114ca7ade1aac51998c3465490f4f3c9b3e868e53590f3fa8601b`。
- RC [v0.0.7-rc.1](https://github.com/liulixin-lex/xy2api/releases/tag/v0.0.7-rc.1) 的 annotated tag object 为 `6b28d99a4faa815ee769f7b7aac2e95db898fb21`，目标为同步 merge commit；Release run [34596879431](https://github.com/liulixin-lex/xy2api/actions/runs/34596879431) 成功。五个制品 SHA-256 全部通过；GHCR digest `sha256:11c2515921c4800ff0a44ff60629573adf0abe1cbc7f2f467eecf22ebc3d9006` 包含 `linux/amd64` 与 `linux/arm64`，OCI version/revision 正确。
- 隔离验证覆盖 RC 全新安装、正式 `v0.0.6` 原地升级、回切旧镜像和再前滚；健康、管理员登录、286 条迁移、四处 MiniMax CHECK 约束、两个主代理共享备用代理的新关系数据，以及 PostgreSQL、Redis、`/app/data` 标记均正常。升级前三类备份已生成且 checksum 稳定；镜像回滚可用，但生产仍必须保留完整备份。
- 正式版 PR [#23](https://github.com/liulixin-lex/xy2api/pull/23) 的固定 head `0bc35438c9f1ffd92334482de25689a678ad4ab6` 在 16/16 检查通过后，以 merge commit `a3c0209184b65a6a9ae1383b725022f92cb9fb6e` 合入；合并后主线 CI run `34600004386` 与安全扫描 run `34600004385` 均成功。
- 正式 [v0.0.7 Release](https://github.com/liulixin-lex/xy2api/releases/tag/v0.0.7) 非 draft、非 prerelease 且为 latest；annotated tag object 为 `64f44b2fa10f7214be4852d3d72cafd1be5b3307`，目标为上述正式 merge commit，Release run [34600960509](https://github.com/liulixin-lex/xy2api/actions/runs/34600960509) 成功。五个制品 SHA-256 全部通过；GHCR `0.0.7`、`latest`、`0.0`、`0` 均指向双架构 digest `sha256:9874af4356e038ff7bd908cf6e26c9389953c6a6b83a712fcee83f557fdd1349`，OCI version/revision 为 `0.0.7` / `a3c0209184b65a6a9ae1383b725022f92cb9fb6e`。
- 正式镜像已用全空 PostgreSQL、Redis 和 `/app/data` 卷完成首次安装及三服务重启验证；版本、健康、登录、迁移、约束与三类持久化数据均符合预期。任务专用容器、卷、网络、镜像、下载文件、构建产物及已合并短期分支已清理，原有 `book-keeper_default` 网络未改动。

### Sub2API v0.2.3 与 XY2API v0.0.6 已完成

以下状态于 `2026-09-08T14:27Z` 通过本地 Git、GitHub Actions、Release、GHCR 和隔离 Docker 环境核实：

- 同步 PR [#17](https://github.com/liulixin-lex/xy2api/pull/17) 的最终 head `b8d84c1d97dbe4916176d8e1dd83d37d82dd7824` 在 16/16 检查通过后，以 merge commit `867097dc4b5cecb60c0b43c85f9a9d8a1e6c6d4c` 合入。固定 Sub2API `v0.2.3` 的 annotated tag object 为 `fe2b5c04b1c9503fba7e01a099b206f14867cfc1`，目标 commit 为 `8fa67d477d6651a744754392a8982ea589c26ae6`，签名状态为 `unsigned`。
- 本次纳入 111 个上游提交与 279 个上游变更文件，人工裁决 21 个冲突；保留 XY2API 认证、部署、长上下文定价和双版本契约，纳入上游模型白名单、simple mode、请求模型归一化与网关修正。详细矩阵见 `docs/upstream-sync/v0.2.3.json`。
- 迁移编号冲突已适配：保持 XY2API 已发布 `235_group_long_context_pricing_models.sql` 及 checksum 不变，将上游迁移顺延为 236/237，追加 238 认证缓存失效迁移。
- RC [v0.0.6-rc.1](https://github.com/liulixin-lex/xy2api/releases/tag/v0.0.6-rc.1) 为 annotated prerelease，Release run [34232579648](https://github.com/liulixin-lex/xy2api/actions/runs/34232579648) 成功。5 个平台归档均通过 `checksums.txt` 复算；GHCR manifest digest 为 `sha256:8ad56a376bd80afba8bb6aee2652afeb3ab904e5ce2415627ff1e09443f9899c`，包含 `linux/amd64` 和 `linux/arm64`，OCI version/revision 为 `0.0.6-rc.1` / `867097dc4b5cecb60c0b43c85f9a9d8a1e6c6d4c`。
- 隔离 Docker 验证覆盖 RC 全新安装，以及从正式 `v0.0.5` 原地升级；健康、管理员登录、运行时版本、新列与 236–238 迁移、模型白名单、长上下文配置、Redis 和业务标记数据均符合预期。
- 升级前对 PostgreSQL、Redis 和 `/app/data` 生成并复算备份；在全新卷上完整恢复后，`v0.0.5` 可重新启动、登录并读取旧列和全部标记数据。因 236 包含列重命名，生产回滚必须同时恢复这三类备份，不能仅切回旧镜像。
- 正式版 PR [#18](https://github.com/liulixin-lex/xy2api/pull/18) 的固定 head `e70d41d5e3e7dbef0d351007be20ca21663a3675` 在 16/16 检查通过后，以 merge commit `e065f39c7bf62ca2634e8d565f59b01131af744b` 合入；该提交的 CI run `34236476773` 与安全扫描 run `34236476766` 成功。
- 正式 [v0.0.6 Release](https://github.com/liulixin-lex/xy2api/releases/tag/v0.0.6) 非 draft、非 prerelease 且为当前 latest，Release run [34236559731](https://github.com/liulixin-lex/xy2api/actions/runs/34236559731) 成功。annotated tag object 为 `edcbacef7d140f67698412f8acd4e3abfbf44df2`，目标为正式 merge commit。
- 5 个正式平台归档全部通过 `checksums.txt` 复算。GHCR `0.0.6` manifest digest 为 `sha256:bc755af87107381281a59201288fd21f7ec367034c0435f222802d7df35b7e70`，包含 `linux/amd64` 与 `linux/arm64`；`0.0`、`0`、`latest` 指向同一 digest，OCI version/revision 为 `0.0.6` / `e065f39c7bf62ca2634e8d565f59b01131af744b`。
- 正式镜像已用全空 PostgreSQL、Redis 和 `/app/data` 卷完成首次安装：运行时版本、健康检查、管理员登录、模型白名单列和 236–238 迁移均符合预期。上游最新正式 Release 在收尾时仍为 `v0.2.3`。

### 分组按模型启用长上下文阶梯计费与 XY2API v0.0.5 已完成

以下状态于 `2026-09-05T18:56Z` 通过本地 Git、GitHub Actions、Release 和 GHCR 核实：

- 功能 PR [#13](https://github.com/liulixin-lex/xy2api/pull/13) 的最终 head 为 `1c345b4fd3a1caa65af9afab83a84ea6936a65b8`，全部保护检查通过后以 merge commit `ab9ddee56ad238ec121fbbc9e60e446ad9e2b862` 合入；实现分组阶梯计费的全部模型/指定模型范围、精确与末尾通配匹配、旧分组兼容、统一扣费判断、认证缓存、旧 OpenAI 结算路径、模型广场实付展示和管理表单。
- 新增 migration `235_group_long_context_pricing_models.sql`，旧迁移及既有 checksum 未改写；新字段纳入 Ent、仓储、API、复制、认证快照版本与数据库缓存失效触发器。指定范围内未匹配模型保持基础档，账号开关不能越过分组名单重新启用阶梯。
- 正式版 PR [#14](https://github.com/liulixin-lex/xy2api/pull/14) 将产品版本晋级到 `0.0.5`，兼容基线保持 Sub2API `v0.2.1`；全部保护检查通过后以 merge commit `38cf85ab4a3870d7fc60041d41f60a8b9b4b0e5b` 合入。该提交的主线 CI run `33984747838` 与安全扫描 run `33984747861` 均成功。
- 正式 [v0.0.5 Release](https://github.com/liulixin-lex/xy2api/releases/tag/v0.0.5) 非 draft、非 prerelease，且为当前 latest；Release run `33984811500` 成功。annotated tag object 为 `68aabf28257590906279f457e1442c8bf629c528`，目标为上述正式 merge commit。
- 5 个平台包已下载并通过 `checksums.txt` 的 SHA-256 复算。GHCR `0.0.5` manifest digest 为 `sha256:f7082949109df5f7150d4e4e24eb75476a59e18624d0b3bfa28da2659b9a2415`，包含 `linux/amd64` 与 `linux/arm64`；`0.0`、`0`、`latest` 指向同一 digest，两种架构的 OCI version/revision 均为 `0.0.5` / `38cf85ab4a3870d7fc60041d41f60a8b9b4b0e5b`。

### Sub2API v0.2.1 与 XY2API v0.0.4 已完成

以下状态于 `2026-09-05T12:57Z` 通过本地 Git、GitHub Actions、Release 和隔离 Docker 环境核实：

- 上游 annotated tag object 为 `adc26f68f687685e847bfb997559f48e79cac475`，目标 commit 为 `578785ee7fb35030b094b69624efe25670a36f5f`；标签未签名并已在 provenance 中明确记录为 `unsigned`。命名空间标签 `sub2api/v0.2.1` 保留。
- 预备 PR [#9](https://github.com/liulixin-lex/xy2api/pull/9) 先登记 5 个新人工冲突路径；同步 PR [#10](https://github.com/liulixin-lex/xy2api/pull/10) 的固定 head `8dd45d110dc1832986932178d7b91305feee5fed` 在 16/16 检查通过后，以 merge commit `294528940231ba6ed7764ccdabe4bbdabaa3783d` 合入。9 个冲突已逐项裁决，82 个上游 commit 与 297 个文件变更已纳入审计报告。
- 4 个新增 migration 均为 additive，既有 277 个 checksum 未改写且新增 checksum 已独立复算；迁移总数为 281。新增内容包括 usage log upstream request ID 及索引、channel reasoning multiplier、group Codex models manifest 配置。
- RC [v0.0.4-rc.1](https://github.com/liulixin-lex/xy2api/releases/tag/v0.0.4-rc.1) 的 annotated tag object 为 `17bcf4009ee9241c8d35b0f5f6ecbcbb3519c20e`，目标为同步 merge commit；Release run `33964718089` 成功，5 个平台包 SHA256 全部通过，GHCR digest 为 `sha256:f8e357a3c08b88eda3e6c9c7ca0d8d9119809b8a57553ce7ce324b05112c953c`，包含 `linux/amd64` 与 `linux/arm64`。
- 正式版 PR [#11](https://github.com/liulixin-lex/xy2api/pull/11) 的固定 head `6dd99debce540f07a70f9b45ae9f115b5123333e` 在 16/16 检查通过后，以 merge commit `ae4c010d0a8aed750936b59655bf7ab8f85a777b` 合入；该提交的主线 CI run `33966180480` 和安全扫描 run `33966180489` 均成功。
- 正式 [v0.0.4 Release](https://github.com/liulixin-lex/xy2api/releases/tag/v0.0.4) 非 draft、非 prerelease；annotated tag object 为 `7e38081004ce4dea4079a86eabe63624e76428ba`，目标为上述正式 merge commit，Release run `33966678769` 成功。5 个平台包 SHA256 全部通过；GHCR digest 为 `sha256:a4eddd09a4343fd9b2e276bcac7691db395d49697043262378c2db68cec5f9c1`，包含双架构，OCI version/revision 正确，`latest`、`0.0`、`0` 均指向同一 digest。
- 隔离 Docker 验证覆盖 RC 全新安装、正式 `v0.0.3` 原地升级到 RC、回切 `v0.0.3` 旧镜像和正式 `v0.0.4` 全新安装；健康、初始化、281 条 migration、新 schema、管理员、Redis 与 `/app/data` 持久化均符合预期。任务专用容器、卷、网络、下载文件和镜像已清理，原有 `book-keeper` 资源未改动。
- 同步 merge 后的主线 CI run `33964691713` 曾因 Docker Hub 拉取 Testcontainers Ryuk 时连接被对端重置而失败，不是代码或测试断言失败；后续 PR 两套集成测试及正式 merge 主线集成测试均通过。

### Sub2API v0.2.0 已合入并完成 RC 验证

以下状态于 `2026-09-04T18:12Z` 通过本地 Git、GitHub Actions、Release 和隔离 Docker 环境核实：

- 同步 PR [#5](https://github.com/liulixin-lex/xy2api/pull/5) 的固定 head `b73310ae84d10d675bf6ad7fac0c840559996aea` 已在 16/16 检查通过后，以 merge commit `48f0f0f10b79b32971649e227e4ceb7f8201e4dd` 合入 `main`。
- v0.2.0 固定 annotated tag object 为 `dd07c4d8d484878e617c945cc8bacc304a5a6560`，目标 commit 为 `aa236488351eb71e120fc2b6fb32e36b0374c918`；标签未签名，provenance 明确记录为 `unsigned`。
- 7 个冲突已逐项裁决；4 个 additive migration 的 checksum 独立复算一致，迁移记录从 273 增至 277，实际新增 7 个字段；既有 migration 未改写。
- RC [v0.0.3-rc.1](https://github.com/liulixin-lex/xy2api/releases/tag/v0.0.3-rc.1) 为 annotated prerelease，Release run `33903033269` 成功；5 个平台包 SHA256 全部通过，GHCR manifest digest 为 `sha256:aedd8c30a43deda75ae4825678ba39a1cc9c16264b9c5548384eeb700944818f`，包含 `linux/amd64` 与 `linux/arm64`。
- 隔离验证中，RC 全新安装健康；从正式 `v0.0.2` 原地升级后用户、Redis 与 `/app/data` 数据保留；再回切 `v0.0.2` 镜像仍健康，数据库与数据可读。

### 自动同步失败已修复

- 2026-09-02 至 09-04 的失败由两个问题叠加：v0.2.0 新冲突路径不在旧 policy 的人工清单中，随后错误报告又尝试写入已关闭的 GitHub Issues，掩盖了首个阻断原因。
- 工作流现在将阻断写入 Job Summary、上传日志 artifact、显式返回原始失败；已有同步 PR 同时按 `upstream-sync` 标签与 `sync/sub2api-` 分支前缀识别。
- 另修复 GitHub Actions 对 skipped 步骤空输出进行宽松数值比较导致误执行 push 的问题，所有后续步骤同时要求 sync step 的 outcome 为 success。
- 分支场景回归 run `33901867104` 与 `main` 已同步场景 run `33902956072` 均成功；两次都只执行必要的选择/标签核验，仓库写操作按预期跳过。

### v0.0.3 正式发布已完成

- 正式版 PR [#7](https://github.com/liulixin-lex/xy2api/pull/7) 的固定 head `bad95a10a6267e2397274324f06a581b67839fba` 在 16/16 检查通过后，以 merge commit `a0e68c6e4f649fc58f95bed78aa1b883ab349cf8` 合入。
- annotated tag object 为 `bf25502f68bf0a49e6fa73048970ac257d7c0d16`，目标为上述 merge commit；正式 [v0.0.3 Release](https://github.com/liulixin-lex/xy2api/releases/tag/v0.0.3) 非 draft、非 prerelease，run `33905898627` 成功。
- 5 个平台压缩包的 SHA256 已独立复算通过；GHCR manifest digest 为 `sha256:14d79a3cd5f6ef29e96e503f9f60b520f806c1f6f5b5050ff5607d7f43a89a1d`，包含 `linux/amd64` 与 `linux/arm64`，OCI version/revision 为 `0.0.3` / `a0e68c6e4f649fc58f95bed78aa1b883ab349cf8`。
- 正式镜像已用全空 PostgreSQL、Redis 和 `/app/data` 卷完成首次安装：健康、setup completed、277 条 migration、1 个管理员、7 个 v0.2.0 新字段齐全。
- RC/正式验证的临时容器、卷、网络和本次新拉取镜像已删除；原有 `book-keeper` 容器、网络、卷和镜像未改动。已合并的记忆、同步、发布短期分支已在本地与远端删除。

### 文档冲突与操作陷阱

- `DEV_GUIDE.md` 的“Git 操作”仍写着直接 `git merge upstream/main` 和 `git rebase upstream/main`。这与正式同步规范冲突，处理 Sub2API 同步时不得照此执行；以 `docs/UPSTREAM_SYNC.md`、同步 Skill、policy 和脚本为准。
- 当前 shell 没有 `python` 命令，仓库 Python 工具在本环境应使用 `python3`；GitHub Actions 中现有 `python` 命令不代表本地也有同名别名。
- `sync.py audit` 强制要求干净工作树。实时更新本文件会产生改动；需要最终上游审计时，应先审查并提交本文件和同批变更，再在干净提交上运行 audit。
- 本地执行 `prepare` 若遇到清单外冲突，会 abort merge，但脚本在 abort 前已经写出 report 文件，可能留下未跟踪/已修改报告；失败后先检查 `git status`，不要误以为工作树自动恢复完全干净。
- `tools/upstream-sync/policy.json` 的 `xy_owned` 范围较宽（包括大部分 `backend/internal/**`、`frontend/**`、`docs/**`）。audit 通过只证明差异被分类、固定契约与具名补丁仍在、migration/provenance 合法，不等于所有二开语义已经自动审查；重叠文件仍需人工核对。
- 当前 GoReleaser manifest 配置会让 RC 同时更新 GHCR `latest`、主版本和次版本别名；本次 RC 发布后这些别名曾短暂指向 RC，已在正式发布后全部恢复为 `v0.0.7` digest。后续应单独调整发布策略，避免 prerelease 改写稳定别名。
- GitHub Actions 在本次 RC/正式 Release 中提示 `docker/login-action@v3`、`docker/setup-buildx-action@v3`、`docker/setup-qemu-action@v3` 的 Node.js 20 runtime 已弃用，Runner 强制切到 Node.js 24 后仍成功；这是后续 Action 版本维护项，不是本次发布阻塞。

## 项目速览

### 产品与架构

- XY2API 是 Sub2API 的二开 AI API 网关，负责多上游账号调度、API Key、配额/计费、协议转发、管理后台等。
- 后端位于 `backend/`：Go `1.27.0`、Gin、Ent、Wire，依赖 PostgreSQL 与 Redis；入口为 `backend/cmd/server/main.go`，前端构建产物嵌入 `backend/internal/web/dist/`。
- 前端位于 `frontend/`：Vue 3 + TypeScript + Vite + Pinia + Vue Router，包管理器固定为 pnpm。
- `deploy/` 保存 Compose、镜像与部署脚本；`openspec/` 保存重要功能规格和验证证据；`skills/` 保存面向 Agent 的项目技能。
- 修改 Ent schema 后需重新生成 `backend/ent/`；修改 Wire provider 后需重新生成 `backend/cmd/server/wire_gen.go`。生成产物必须和源定义一起审查、提交。

### 常用验证入口

```bash
# 仓库同步工具与升级预检单测
python3 -m unittest discover -s tools/tests -p 'test_*.py' -v

# 只应在干净工作树执行
python3 tools/upstream-sync/sync.py audit

# 上游同步环境体检；准备新同步前要求 strict 通过
python3 skills/xy2api-upstream-sync/scripts/doctor.py --strict

# 后端（需要 go 1.27.0）
cd backend && go test -tags=unit ./...
cd backend && go test -tags=integration ./...

# 前端（需要 pnpm）
pnpm --dir frontend run lint:check
pnpm --dir frontend run typecheck
pnpm --dir frontend run test:run
pnpm --dir frontend run build
```

根 `Makefile` 提供 `build`、`test`、`test-backend`、`test-frontend`；后端 `Makefile` 提供 `build`、`generate`、`test-unit`、`test-integration` 和 `test-e2e`。验证前应按变更范围选择，不要机械运行无关长测试。

## 上游同步机制摘要

### 事实来源

| 文件 | 职责 |
| --- | --- |
| `skills/xy2api-upstream-sync/SKILL.md` | 阶段选择、授权边界、停止条件和交付证据 |
| `skills/xy2api-upstream-sync/references/runbook.md` | prepare、人工裁决、PR、RC、正式版和收尾执行手册 |
| `docs/UPSTREAM_SYNC.md` | 项目正式同步规范与兼容不变量 |
| `tools/upstream-sync/sync.py` | `report`、`prepare`、`normalize`、`audit` 实现 |
| `tools/upstream-sync/policy.json` | 所有权分类、兼容字面量、禁止回流文件、二开补丁与绑定测试 |
| `UPSTREAM_BASE.json` | 当前 main 已审计上游基线与不可变 provenance |
| `docs/upstream-sync/<tag>.json` | 每次同步的三方文件矩阵和影响报告 |
| `.github/workflows/upstream-sync.yml` | 选择稳定 release、固定标签、准备分支与 Draft PR |
| `.github/workflows/release.yml` | 校验产品版本标签并构建/发布制品，不负责改版本文件 |

### 标准流程

1. 只选择正式、非 draft、非 prerelease 的 semver annotated tag；保存官方 tag object，并固定到 `refs/tags/sub2api/<tag>`。
2. 冻结目标 commit、当前 fork 起点和真实 merge base；禁止跟随移动的 `upstream/main`。
3. `report` 计算上游与二开两侧 commit、变更文件、重叠矩阵、所有权和 migration/API/config/dependency/generated 影响。
4. `prepare` 要求干净工作树，执行 `git merge --no-ff --no-commit`。清单外冲突立即阻断；清单内冲突先保留 XY2API 侧并登记 `pending`，不能把它当作已解决。
5. 官方 merge 单独提交；仅对本次上游变更涉及的 `.go`/`.proto` 做 module path 字节替换并单独提交。禁止全仓 `sub2api -> xy2api` 替换。
6. 人工逐项比较 base/upstream/XY2API，按 `UPSTREAM`、`XY_OWNED`、`COMPAT_INVARIANT`、`MANUAL_MERGE`、`GENERATED` 五类裁决；更新 provenance、兼容版本、migration manifest、二开补丁和生成代码。
7. `audit` 检查干净工作树、冲突标记、已发布 migration checksum、policy 结构、具名补丁测试、必需/禁止契约、module path、标签/祖先关系、双版本一致性、pending 状态和未分类差异。
8. 自动化最多创建一个 Draft PR，从不自动 merge 或 release。同步合入后先做 RC，隔离验证制品/升级/回滚，再用独立正式版 PR 晋级产品版本。

### 不可破坏的兼容边界

- 已发布 migration 及 `backend/migrations/checksums.json`：既有 SQL/hash 不得改写，只能追加新编号与 checksum。
- 插件 v1 包名、Magic Cookie、`requires.sub2api` 等第三方兼容字段继续使用 Sub2API 语义。
- 已发布 HTTP 路由、Redis/浏览器 key、稳定 UUID/KDF 输入、WebSocket 子协议、旧配置目录、旧二进制名和 Grok 兼容 header 不因品牌改造重命名。
- `VERSION` 是 XY2API 产品版本；`SUB2API_COMPAT_VERSION` 是已完成审计的 Sub2API 基线，二者独立演进。
- 品牌、仓库、镜像、安装/发布策略和 sponsor-free 政策归 XY2API；同步功能时不能让 CLA、广告或赞助资源回流。
- 冲突裁决、二开兼容补丁、生成产物、provenance、RC/正式版本应保持可追踪的分层提交。

## 记忆维护规范

### 更新时机

- 开始任务：核实 Git 状态并在“进行中的工作”登记。
- 执行中：只在范围、关键事实、决策、文件集合、验证结果或卡点变化时更新。
- 结束前：更新顶部当前状态；将自己的进行中条目删除或标为阻塞；在日志末尾追加记录。
- 外部状态可能变化时（PR、CI、Release、上游 tag、价格/依赖等），记录“核实时间”，后续 Agent 必须重新查询。

### 日志模板

```markdown
### <UTC 时间> — <任务 ID> — <状态>

- 请求/目标：
- 开始状态：分支、HEAD、工作树，以及相关远端标识。
- 完成操作：关键检查、决策和修改，不逐条粘贴命令流水账。
- 修改文件：
- 验证：命令与结果；未运行项及原因。
- 卡点/风险：无则写“无”。
- 下一步：无则写“无”。
```

## 操作日志

<!-- 按时间从旧到新追加。历史记录只允许追加更正，不应静默改写。 -->

### 2026-09-04T16:08:05Z — `20260904-memory-bootstrap` — 完成

- 请求/目标：仔细分析 XY2API 项目及其 Sub2API 上游同步机制，建立可供后续 Agent 实时维护的仓库记忆和开发日志。
- 开始状态：位于 `main`，HEAD `79955dbaa964732429747ae65dabf9a8bfb44a65`，与 `origin/main` 一致，工作树干净；主线版本为 XY2API `0.0.2` / Sub2API compat `0.1.185`。
- 完成操作：阅读项目结构、入口、Makefile、同步规范、Skill/Runbook、policy、同步脚本、Doctor、升级预检、工作流、版本化报告及相关 Git 历史；只读核实远端分支、PR #5、检查结果和近期定时工作流；建立根级 Agent 入口、当前状态区、进行中工作区、长期注意事项和追加式操作日志；将 Agent 入口登记为 `xy_owned`。
- 修改文件：`.gitignore`（允许跟踪两份协作文档）、`AGENTS.md`（强制读取/更新协议）、`docs/PROJECT_MEMORY.md`（本文件）、`tools/upstream-sync/policy.json`（分类 `AGENTS.md`）。未修改业务代码，未执行远端写操作。
- 验证：修改前 `python3 tools/upstream-sync/sync.py audit` 通过；Doctor 的 audit 通过但因缺少 `upstream` remote 而 `ready_for_prepare=false`；修改前后 `python3 -m unittest discover -s tools/tests -p 'test_*.py' -v` 均为 8/8 通过；policy JSON 可解析，`AGENTS.md` 与 `docs/PROJECT_MEMORY.md` 均解析为 `xy_owned`；`git diff --check` 通过；两份新文档已从 `.gitignore` 中显式放行。Go 与 pnpm 当前不可用，且本次仅为文档/策略改动，未运行后端和前端套件。
- 卡点/风险：记忆机制本身无阻塞。另有未获授权处理的既存事项：v0.2.0 Draft PR #5 无 `upstream-sync` 标签，自动同步连续失败，且相关工作流修复仍只在该 PR 分支；详见“当前重要事项”。
- 下一步：维护者可审查并提交本次 4 项改动。后续 Agent 应在每次仓库任务中按 `AGENTS.md` 更新本文件；如要处理 PR #5、修复自动同步或继续 v0.2.0 集成，需先取得对应授权并重新核实远端状态。

### 2026-09-04T16:22:26Z — `20260904-commit-memory-bootstrap` — 完成

- 请求/目标：将记忆机制初始化改动提交到当前本地 `main`。
- 开始状态：HEAD `79955dbaa964732429747ae65dabf9a8bfb44a65`，与 `origin/main` 一致；仅有上一任务产生的 `.gitignore`、`AGENTS.md`、`docs/PROJECT_MEMORY.md`、`tools/upstream-sync/policy.json` 4 项改动。
- 完成操作：重新读取仓库记忆、复核提交范围，更新本次操作日志，并以 `docs: establish repository agent memory` 创建单一聚焦提交。未推送远端。
- 修改文件：仍为上述 4 项记忆机制文件，没有混入业务代码或其他既有改动。
- 验证：`git diff --check` 和 Markdown 尾随空白检查通过；同步工具与升级预检单测 8/8 通过；两份协作文档的 `xy_owned` 分类通过；提交后检查本地工作树状态。
- 卡点/风险：无。
- 下一步：无需继续操作；若要推送，应由用户另行明确要求。后续任务开始时用 Git 核实本次提交 SHA 和工作树状态。

### 2026-09-04T18:12:06Z — `20260904-full-upstream-sync-release-v0.0.3` — 进行中

- 请求/目标：分析同步失败并按最佳规范修复自动化、集成 Sub2API `v0.2.0`、完成 RC 验证并发布 XY2API `v0.0.3`。
- 开始状态：本地 `main` 比 `origin/main` 多一个未推送的记忆提交；Draft PR #5 head 为 `e8bfe57df33b19c099b61794df1a376f712d262c`，缺少同步标签；2026-09-02 至 09-04 的定时同步连续失败。
- 完成操作：通过 PR #6 保护并合入记忆提交；为 PR #5 补标签，修复已有 PR 识别与 skipped-step 守卫，复核固定上游标签、三方差异、7 个冲突和 4 个 migration 后，以 merge commit 合入；手工回归同步工作流；创建并验收 `v0.0.3-rc.1` annotated tag、Release 与双架构镜像；在隔离 Docker 环境完成正式 `v0.0.2` 到 RC 的升级、旧镜像回滚和 RC 全新安装。
- 修改文件：同步 PR 覆盖 `UPSTREAM_BASE.json`、版本文件、同步报告、工作流/policy、4 个新增 migration、上游功能与测试；正式版分支当前仅准备修改 `backend/cmd/server/VERSION`、`UPSTREAM_BASE.json` 与本记忆文件。
- 验证：同步工具 9/9、provenance/compatibility audit、Compose 解析、Go 1.27.0 Ent/Wire 零差异、PR #5 的 16/16 GitHub checks、两个同步回归 run、RC Release run、5 个平台包 SHA256、GHCR amd64/arm64、全新安装、原地升级和旧镜像回滚均通过。
- 卡点/风险：无当前阻塞；正式版本尚未发布。
- 下一步：完成正式版 PR、`v0.0.3` 标签与产物/镜像/启动验收，清理隔离资源和已合并短期分支，再将本条记录更新为完成。

### 2026-09-04T18:43:33Z — `20260904-full-upstream-sync-release-v0.0.3` — 完成

- 请求/目标：分析 GitHub 上游同步失败，按最佳规范修复同步链路，集成 Sub2API `v0.2.0` 并发布 XY2API `v0.0.3`。
- 开始状态：本地有未推送的记忆提交；Draft PR #5 缺少同步标签；旧 policy 未覆盖 v0.2.0 新冲突，阻断报告又写入已关闭的 Issues，导致 2026-09-02 至 09-04 定时同步连续失败。
- 完成操作：通过 PR #6 纳入记忆机制；修复冲突清单、Job Summary/artifact 报告、已有 PR 识别与 skipped-step 守卫；完成固定标签 provenance、三方差异和 7 个冲突人工裁决，通过 PR #5 合入上游；完成两个同步回归；发布并验收 `v0.0.3-rc.1`；通过独立 PR #7 晋级并发布 `v0.0.3`；删除已合并短期分支和全部任务专用 Docker 资源。
- 修改文件：同步与上游功能修改详见 PR #5；正式晋级修改 `backend/cmd/server/VERSION`、`UPSTREAM_BASE.json` 和本记忆文件；本收尾提交只更新本记忆文件。
- 验证：PR #5 与 #7 各 16/16 checks；同步/升级预检 9/9；provenance/compatibility audit、Compose、Ent/Wire 零差异；同步回归 runs `33901867104`、`33902956072`；RC/formal Release runs `33903033269`、`33905898627`；两版各 5 个资产 SHA256；两版 GHCR amd64/arm64 与 OCI 标签；RC 全新安装、v0.0.2 原地升级和旧镜像回滚；正式版全新安装均通过。
- 卡点/风险：无已知阻塞。4 个新 migration 为 additive，旧镜像已验证可在升级后的 schema 上运行；数据库回滚仍应继续采用升级前备份策略，不宣称 SQL 自动降级。
- 下一步：无。后续常规维护应保留 `sub2api/v0.2.0`、`v0.0.3-rc.1`、`v0.0.3` 标签，并从本文件、`UPSTREAM_BASE.json` 与 GitHub Release 重新核实动态状态。

### 2026-09-05T12:57:29Z — `20260905-full-upstream-sync-v0.2.1` — 完成

- 请求/目标：按仓库标准全流程同步 Sub2API 最新正式版 `v0.2.1`，完成审计、PR 合入、RC/正式发布验证与收尾。
- 开始状态：本地 `main` 为 `90c46b18dcc0e2146e2e210d6e6b633cfaef07ef`，与 `origin/main` 一致且工作树干净；XY2API `0.0.3` / Sub2API compat `0.2.0`；`doctor.py --strict` 通过；上游最新稳定 Release 为 `v0.2.1`。
- 完成操作：冻结 annotated tag object、目标 commit、fork 起点和 merge base；通过预备 PR #9 扩充具名人工冲突策略；生成三方影响报告并以真实 merge 集成 82 个上游 commit，逐项裁决 9 个冲突，归一化模块路径、复算 4 个新增 migration checksum、确认生成产物零差异；同步 PR #10、正式版 PR #11 均在固定 head 16/16 检查通过后以 merge commit 合入；创建并验收 `v0.0.4-rc.1` 与 `v0.0.4` annotated Release；删除已合并短期分支和全部任务专用环境资源。
- 修改文件：同步改动与完整矩阵见 `docs/upstream-sync/v0.2.1.json` 和 PR #10；正式晋级仅修改产品版本、provenance 产品版本和本记忆文件；本次收尾仅修改本记忆文件。
- 验证：同步/升级预检 10/10；strict doctor、provenance/compatibility audit、diff/冲突标记、Compose、祖先关系与 migration checksum 通过；PR #10、#11 各 16/16 checks；正式 merge 主线 CI/安全扫描通过；RC/正式 Release runs `33964718089`、`33966678769` 成功；两版各 5 个资产 SHA256、GHCR amd64/arm64 和 OCI 标签通过；RC 全新安装、`v0.0.3` 原地升级、旧镜像回切及正式版全新安装通过。
- 卡点/风险：无已知阻塞。Go 1.27.0 容器中完整重复生成受 4 GiB 内存限制曾被 SIGKILL，但单并发唯一 Ent/Wire 生成成功且产物零差异，CI 编译、测试与 lint 均通过。4 个新 migration 为 additive，旧镜像回切已验证；生产数据库回滚仍应使用升级前备份，不宣称 SQL 自动降级。同步 merge 的一次主线集成测试因 Docker Hub 拉取 Ryuk 时连接重置失败，后续三套等价集成测试均成功。
- 下一步：无。后续维护应保留 `sub2api/v0.2.1`、`v0.0.4-rc.1`、`v0.0.4` 标签，并从本文件、`UPSTREAM_BASE.json` 与 GitHub Release 重新核实动态状态。

### 2026-09-05T17:02:30Z — `20260905-group-model-long-context-pricing` — 完成

- 请求/目标：让分组只对指定模型启用长上下文阶梯计费，并让实际扣费、认证缓存与模型广场实付价格保持一致；支持精确名和末尾 `*` 前缀匹配，旧分组继续按全部模型处理。
- 开始状态：从干净 `main` 的 `078820857b1c3036769777a7956d17dba431d464` 新建 `feat/group-model-long-context-pricing`；本地无 Go、pnpm 命令，Docker、Node 与 Corepack 可用。
- 完成操作：新增分组范围和模型名单字段、迁移与 Ent 生成代码；在统一定价解析层实现模型匹配和账号开关约束，并覆盖统一、OpenAI 旧计费与模型广场链路；更新认证查询、快照版本和数据库失效触发器；创建、编辑、读取、复制分组完整传递配置；新增管理表单的范围选择、搜索/手动模型标签输入及模型广场部分启用说明。
- 修改文件：后端涉及 `backend/ent/schema/group.go`、生成的 `backend/ent/`、`backend/migrations/235_group_long_context_pricing_models.sql`、分组管理/仓储/认证缓存/计费/模型广场服务及测试；前端涉及分组类型与管理页、新增 `LongContextPricingFields.vue`、账号提示逻辑、模型广场说明和中英文文案；同步更新本记忆文件。
- 验证：Go 1.27 Docker 下完整 `internal/service` 单测通过，新增定向 service 测试通过，`internal/handler`、`internal/repository`、`internal/server`、`migrations` 单测通过，真实 PostgreSQL 集成测试 `TestAuthCacheInvalidationTrigger_ProfitControlColumns` 通过；前端定向 14/14 测试、类型检查、全量 lint 和生产构建通过；Ent 再生成前后差异 SHA256 均为 `f16ec3a90a0b20689a0ff7d12430396d98e43dc5d2263a6efb190767fbe33ddd`；新增迁移 checksum 复算一致；Playwright Chromium 在 1440×1000/1100×760 桌面和 390×844 移动视口检查模型广场与真实表单组件，无重叠或布局回归。
- 卡点/风险：无功能阻塞。默认 Go 编译在 4 GiB 环境中曾因超大 Ent 包被 SIGKILL，改用单并发、关闭测试阶段 vet、禁用内联并降低 GC 阈值后全部通过；Impeccable detector 仅报告 `GroupsView.vue` 四处既有红底灰字警告，本次新增组件无命中。
- 下一步：审查并提交当前特性分支；本任务未推送、未发布、未部署。

### 2026-09-05T18:56:06Z — `20260905-release-group-model-long-context-pricing` — 完成

- 请求/目标：规范提交并推送分组按模型启用长上下文阶梯计费功能，使用本机 GitHub CLI 凭据通过受保护分支合入，并正式发布 XY2API `v0.0.5`。
- 开始状态：位于 `feat/group-model-long-context-pricing`，功能实现和本地验证已完成但尚未提交；`main` 干净，正式版本为 `v0.0.4`，分支保护要求 CI、安全扫描和集成测试通过后才能合入。
- 完成操作：提交并推送功能改动 `2e5218621`；首次 CI 暴露缓存失效集成测试将布尔字段写为旧默认值的问题，改为确定性翻转布尔值并写入非空模型价格后，提交修正 `1c345b4fd`；通过 PR #13 合入功能。随后在独立分支将产品版本晋级到 `0.0.5`，保持 Sub2API 兼容版本 `0.2.1`，以提交 `d6fe27de0` 经 PR #14 合入。创建并推送 annotated tag `v0.0.5`，完成正式 GitHub Release、平台制品和 GHCR 镜像发布。
- 修改文件：功能提交覆盖分组 schema/迁移/Ent 生成代码、管理 API 与仓储、认证缓存、统一及旧计费路径、模型广场服务、管理前端与测试；版本晋级修改 `backend/cmd/server/VERSION`、`UPSTREAM_BASE.json` 和本记忆文件；本次收尾仅修改本记忆文件。
- 验证：功能与发布 PR 的全部必需 CI/安全检查通过；真实 PostgreSQL 缓存失效集成测试通过；正式 merge 的 CI run `33984747838`、安全扫描 run `33984747861` 及 Release run `33984811500` 成功；Release 为正式 latest，5 个平台包 SHA-256 全部通过；远端标签为 annotated tag 并指向 `38cf85ab4`；GHCR `0.0.5`、`0.0`、`0`、`latest` 均为相同 amd64/arm64 manifest，OCI 版本和提交标签正确。
- 卡点/风险：无已知阻塞。首次 CI 失败来自测试未保证更新值发生变化，修正后本地真实 PostgreSQL 测试及后续两套 PR 检查均通过。新增 migration 为 additive；生产升级仍应按 Release 说明先备份 PostgreSQL、Redis 与 `/app/data`。
- 下一步：无。后续维护从 `main` 开始，并保留 `v0.0.5` 标签与 Release；动态状态以 GitHub 和 GHCR 重新核实为准。

### 2026-09-08T14:27:42Z — `20260908-sub2api-v0.2.3-full-release` — 完成

- 请求/目标：分析 GitHub 仓库与上游同步状态，按仓库标准完成 Sub2API 最新正式版同步、审计、提交、RC 验证与 XY2API 正式发版全流程。
- 开始状态：`main` 为 `5d96e0fc203145981c7eb2ddc78e226ad1502b22`，与 `origin/main` 一致且工作树干净；XY2API `0.0.5` / Sub2API compat `0.2.1`；无开放 PR。定时同步正常，但上游在其上次运行后新发布了 `v0.2.3`，因此仓库处于正常的新版本待同步状态。
- 完成操作：固定并二次核对上游 annotated tag object、目标 commit、fork 起点和 merge base；生成三方影响报告；通过预备 PR #16 登记新冲突，合并 111 个上游提交并逐项裁决 21 个冲突；完成模块路径、Ent/Wire、迁移编号与 checksum、provenance 适配，通过同步 PR #17 合入。随后发布并验收 `v0.0.6-rc.1`，通过独立版本 PR #18 晋级并发布正式 `v0.0.6`，清理任务专用容器、卷、网络、预览 worktree 和已合并短期分支。
- 修改文件：完整同步矩阵见 `docs/upstream-sync/v0.2.3.json` 和 PR #17；预备策略见 PR #16；正式版本晋级仅修改 `backend/cmd/server/VERSION`、`UPSTREAM_BASE.json` 和本记忆文件；本次收尾仅更新本记忆文件。
- 验证：同步工具 11/11、strict doctor、provenance/compatibility audit、冲突标记与 module path 检查、Compose/部署脚本；Go 1.27 unit/integration、真实 PostgreSQL/Redis repository 集成；前端 lint、typecheck、1901 项测试和生产构建；PR #16、#17、#18 所有必需检查；RC/正式 Release runs `34232579648`、`34236559731`；两版各 5 个制品的 SHA-256；双架构 GHCR 与 OCI 标签；RC 全新安装、`v0.0.5` 原地升级、PostgreSQL/Redis/`/app/data` 完整备份恢复回滚，以及正式版全新安装均通过。
- 卡点/风险：无已知阻塞。上游迁移编号 235 与 XY2API 已发布迁移冲突，已通过只追加 236–238 解决，旧 235 及 checksum 未改写。由于 236 包含列重命名，生产回滚必须恢复升级前 PostgreSQL、Redis 和 `/app/data` 备份，不能仅切换回旧镜像。
- 下一步：无。后续维护应保留 `sub2api/v0.2.3`、`v0.0.6-rc.1` 和 `v0.0.6` 标签，从 `main`、`UPSTREAM_BASE.json` 与 GitHub Release 重新核实动态状态。

### 2026-09-11T12:24:47Z — `20260910-sub2api-v0.2.4-full-release` — 进行中

- 请求/目标：分析 GitHub 仓库同步状态，按仓库标准完成 Sub2API 下一正式版同步、审计、提交、RC 与 XY2API 正式发版全流程。
- 开始状态：`main` 为 `33d05a4`，与 `origin/main` 一致且工作树干净；XY2API `0.0.6` / Sub2API compat `0.2.3`；同步机制正常，上游已新发布正式 `v0.2.4`，因此处于正常待同步状态。
- 完成操作：固定上游 annotated tag object、目标 commit、fork 起点与 merge base；通过预检 PR #21 登记 3 个新冲突路径；生成三方报告并集成 70 个上游提交，裁决 7 个冲突，完成模块归一、迁移编号/checksum、生成代码与 provenance 适配；同步 PR #22 在 16/16 检查通过后以 `ea48f08fc8` 合入；发布并验收 `v0.0.7-rc.1`。
- 修改文件：完整同步矩阵见 `docs/upstream-sync/v0.2.4.json` 和 PR #22，预检策略见 PR #21；当前正式版候选分支仅修改 `backend/cmd/server/VERSION`、`UPSTREAM_BASE.json` 和本记忆文件。
- 验证：同步工具 13/13、strict doctor/audit、Compose/部署脚本、Go 1.27 Ent/Wire 零差异、unit/integration/build、前端 lint/typecheck/i18n/1,975 tests/build、PR #21/#22 全部检查及合并后主线 CI/安全扫描均通过。RC Release run `34596879431` 成功，五个平台制品的 SHA-256 全部通过，GHCR digest `sha256:11c2515921c4800ff0a44ff60629573adf0abe1cbc7f2f467eecf22ebc3d9006` 包含 amd64/arm64 且 OCI version/revision 正确。隔离环境中 RC 全新安装、`v0.0.6` 原地升级、旧镜像回切、再前滚、286 条迁移、MiniMax 约束、共享备用代理关系、登录和三类持久化数据均通过；升级前 PostgreSQL/Redis/`/app/data` 备份已生成并复算稳定。
- 卡点/风险：无当前阻塞。迁移 239 只扩展 CHECK 约束，旧镜像已验证可在升级后 schema 与新关系数据上运行；生产仍应保留升级前三类备份，不宣称 SQL 自动降级。
- 下一步：通过独立正式版 PR 晋级 `0.0.7`，验收正式 Release/镜像/新装，清理隔离资源与已合并短期分支，再将本记录更新为完成。

### 2026-09-11T13:09:37Z — `20260910-sub2api-v0.2.4-full-release` — 完成

- 请求/目标：分析 GitHub 仓库与同步状态，按仓库标准完成 Sub2API 下一正式版的预检、三方合并、审计、PR、RC、升级/回滚验收、正式发布与清理全流程。
- 开始状态：`main` 为 `33d05a4`，与 `origin/main` 一致且工作树干净；XY2API `0.0.6` / Sub2API compat `0.2.3`；无开放 PR。自动同步机制正常，但上游已在上次检查后发布 `v0.2.4`，因此处于正常的新版本待同步状态。
- 完成操作：固定并二次核对上游 tag object、目标 commit、fork 起点与 merge base；通过 PR #21 完成策略预检，通过 PR #22 完成 70 个上游提交、7 个冲突、迁移/provenance/生成代码适配；发布并验收 `v0.0.7-rc.1`；通过独立 PR #23 晋级并发布正式 `v0.0.7`；完成所有任务资源与已合并分支清理。
- 修改文件：完整同步改动与矩阵见 `docs/upstream-sync/v0.2.4.json` 和 PR #22；预检策略见 PR #21；正式晋级仅修改 `backend/cmd/server/VERSION`、`UPSTREAM_BASE.json` 和本记忆文件；收尾仅更新本记忆文件。
- 验证：同步工具 13/13、strict doctor/audit、上游标签/祖先/provenance、Compose/部署脚本、Go 1.27 Ent/Wire 零差异、unit/integration/build、前端 lint/typecheck/i18n/1,975 tests/build；PR #21/#22/#23 的全部必需检查与两次合并后主线 CI/安全扫描；RC/正式 Release runs `34596879431` / `34600960509`；两版各五个制品 SHA-256、双架构 GHCR 与 OCI 标签；RC 全新安装、`v0.0.6` 升级/回滚/再前滚、正式版全新安装及三服务重启持久化均通过。
- 卡点/风险：无已知功能阻塞。迁移 239 为只追加的 CHECK 约束扩展，旧镜像回切已验证；生产仍应备份三类数据。RC 暂时改写稳定 GHCR 别名与 Release Action runtime 弃用提示已登记为后续发布维护项，本次正式别名与发布结果均正常。
- 下一步：无。后续维护应保留 `sub2api/v0.2.4`、`v0.0.7-rc.1` 和 `v0.0.7` 标签，从 `main`、`UPSTREAM_BASE.json` 与 GitHub Release 重新核实动态状态。

### 2026-09-13T19:41:31Z — `20260913-openai-iq-check` — 源码实施与功能验证完成

- 请求/目标：按用户批准方案实现 OpenAI 周期糖果题检测；用户随后明确允许最终答案为21的解释文字，优先要求简洁JSON，并要求将实现写入真实仓库的标准路径。
- 开始状态：`main` / `20c307a9b4a6ee1a8ef126d9aa0baa9aa5c500e6`，原工作树干净。最初在独立副本实施；随后按最新要求写入 `/xy/xy2api`，origin 已确认是 `github.com/liulixin-lex/xy2api`。原始源码归档保留于交付目录 `BASELINE.tar.gz`。
- 完成操作：新增 `Account.IQCheck` 独立调度限制、默认关闭/15分钟的配置、三态筛选、最多10个跨实例租约、120秒请求超时、两轮记录事务保留、失效旧任务保护；复用 OAuth/代理/TLS/插件传输。请求固定 `gpt-6-astra` / `low`、`store:false`，要求整数 `answer` JSON，不注入标准答案且不携带历史；兼容明确解释性结论。创建/更新/批量设置、管理员接口、蓝色开关、频率编辑、立即检测、历史弹窗与中英文文案已接入。
- 变更位置：`backend/internal/pkg/iqcheck/`、`service/iq_check_service.go`、`repository/account_iq_check.go`、账号 DTO/路由与缓存、追加迁移240及校验清单、Ent/Wire生成文件；前端沿用账号页面及组件目录。使用说明为 `docs/OPENAI_IQ_CHECK.md`。
- 验证：判分与服务定向测试通过，覆盖JSON/解释/歧义数字、完整与中断响应、OAuth/Setup Token/API key、固定参数、独立请求、异常状态、人工限制与旧缓存候选。真实 PostgreSQL 集成验证了状态过滤、两轮保留、关闭/重开/换凭据、正常刷新、软删除、跨实例10租约和过期恢复。前端269个测试文件、1977项测试通过，lint、类型/i18n检查与生产构建通过；Go服务端构建通过。原有迁移校验值全部不变。
- 交付：固定四角色为 `/xy/artifacts/openai-iq-check/MODIFIED_FILE.tar.gz`、`DIFF_FILE.patch`、`VERIFICATION.txt`、可执行 `ROLLBACK.sh`。事务脚本在隔离副本上对相同模拟账号状态输入运行原版、修改版、回滚版，验证原始源码哈希及可确定重建的归档哈希；实际状态与字面输出以 `checkpoint.json` 和 `VERIFICATION.txt` 为准。回滚脚本恢复源码，不是数据库降级脚本。
- 限制：未调用真实账号。浏览器自动化返回 `browser_no_host`，未进行真实浏览器视觉验收；移动端沿用现有 DataTable 的同名单元格插槽。测试期间因4GB内存压力调整为串行构建；已保留失败及重跑日志，不将中断的编译记为通过。
- 后续：代码可从本地工作区审阅；本任务未授权推送、发布或部署。需要继续交付核验时执行上述事务脚本，它会复用已确认阶段。

### 2026-09-14T03:50:45Z — `20260914-openai-iq-check-v0.0.8-release` — 完成

- 请求/目标：使用本机 GitHub 登录态提交、推送智商检测功能，并发布下一正式版。
- 开始状态：`main` / `20c307a9b4`，功能已在工作区实现和验证，产品版本 `0.0.7`，尚未推送。用户本轮明确授权提交、推送、合入和发版。
- 完成操作：以本机 `liulixin-lex` 登录态推送 `release/0.0.8`，通过受保护 PR #25 合入功能、版本晋级及测试修复；推送 annotated `v0.0.8`，由现有 Release 工作流生成正式 latest Release 和镜像；下载制品并校验，更新仓库记忆。
- 修改文件：功能覆盖 backend/frontend 标准目录、追加 migration 240、Ent/Wire；版本修改 `VERSION` 与 `UPSTREAM_BASE.json`；发布修复覆盖 `wire_gen_test.go`、账号 SQL mock、IQ 集成清理及错误返回值显式处理。本次收尾只更新本文件。
- 验证：PR 最终16/16检查、主线CI/安全扫描与Release均成功；本机完整仓储单测/集成包、服务清理回归通过；5个平台包SHA-256、Linux版本输出、双架构OCI标签和稳定镜像别名均通过。相同模拟输入完成 BASELINE/MODIFIED/ROLLBACK，补丁可确定重建、回滚源码哈希恢复一致；固定四角色文件保留完整命令和字面日志。
- 卡点/风险：无发布阻塞。前几轮CI失败和本机过时静态检查中止均保留日志，未将失败记为通过。未进行本轮生产升级或真实账号检测；浏览器视觉验证限制沿用实现记录。
- 下一步：无待发布事项；后续从 `main` 和正式 `v0.0.8` 重新核实动态状态。原始源码归档和回滚脚本用于源码恢复，不是数据库降级。

### 2026-09-14T07:31:07Z — `20260914-iq-stability-plan` — 规划完成

- 请求/目标：规划规范稳定的糖果题检测；追加在编辑间隔旁配置模型及思考深度，支持账号模型拉取与自定义输入。
- 开始状态：`main` / `dff987fb16d206e354a21bd4055987c2b5d73549`，工作树干净；四角色交付来自上一版完成状态。
- 完成操作：核对请求、判分、租约、状态及模型发现接口，实际运行当前解析器边界审计；新增 OpenSpec proposal/design/tasks/spec，明确 JSON/纯数字兼容、精确答案提取、未知原因、只读模型目录、可配置 model/reasoning_effort、旧字段省略保留、任务快照及两轮保留。
- 关键发现：歧义答案、重复 JSON 键和浮点精度可能误判聪明，解释中的反例可能误判降智；只改间隔会重置状态；模型目录可能回退默认模型，同步接口会写账号元数据，方案将提取只读公共逻辑。
- 修改文件：只新增 `openspec/changes/stabilize-openai-iq-check/` 中4份方案文档并更新本文件；业务源码、数据库与发布版本未修改。
- 验证：当前解析器副本审计命令退出0，结果是已复现缺陷而非新功能通过；方案链接、JSON示例和20个 WHEN/THEN 场景结构检查通过，git diff --check通过。OpenSpec CLI未安装，未声称运行官方校验器；首次自写检查误设场景数量17导致退出1，修正为检查场景结构后通过，失败记录保留。固定四角色继续由事务脚本收纳，字面 BASELINE/MODIFIED/ROLLBACK 及哈希以 VERIFICATION.txt 为准。
- 边界：单次判定和未知解除自身限制保持；无法可靠提取答案改为未知属于拟议行为变更。未调用真实账号，未实现或发布本改进；模型实际支持及任意自然语言准确识别不作保证。
- 下一步：实施任务见 `openspec/changes/stabilize-openai-iq-check/tasks.md`；不要把方案中的未来行为当作当前代码已实现。


### 2026-09-14T09:14:35Z — `20260914-iq-stability-implementation` — 完成

- 请求/目标：执行稳定检测改进方案，账号编辑间隔旁提供自定义模型与思考深度；接受明确最终答案21的解释文字，规范简洁输出并防止歧义误判。
- 开始状态：`main` / `dff987fb16d206e354a21bd4055987c2b5d73549`，已有4份OpenSpec方案和记忆改动；在 `feat/iq-check-stability` 实施。前一轮四角色完整备份于 `/xy/artifacts/openai-iq-check/pre-stability-implementation/`；原始源码归档不变。
- 完成操作：`IQCheckSettings` 可选字段 `model/reasoning_effort/output_mode`，默认 Astra/low/compat；只读模型目录、5分钟缓存、来源标记、自定义值和上游默认省略参数；candy-v2 精确答案提取与拒答/冲突/重复JSON/精度边界；任务快照、配置失效、只改间隔保留状态、租约过期释放和周期抖动；双协议规范输出与严格Schema；记录规范化答案及原文折叠；复制/导入保留配置但关闭检测；批量仅修改勾选字段。
- 文件位置：沿用 `backend/internal/domain/iq_check.go`、`pkg/iqcheck/`、`service/iq_check*.go`、`repository/account_iq_check.go`、原账号管理路由/处理器，前端原组件/types/api/i18n目录。追加迁移241，既有SQL及checksum不变；JSON域与SQL记录表不改变Ent/Wire结构或依赖，未制造生成代码变更。使用及待发布说明写入已跟踪的 `docs/OPENAI_IQ_CHECK.md`。
- 验证：最终Go相关单测（含配置、三协议模拟、目录只读、精确数值、复制和导入导出）通过；完整repository PostgreSQL/Redis集成通过；前端原相关118项、补充59项（包含重叠复验）及记录弹窗2项通过；全仓lint、类型/i18n、前端生产构建及服务端构建通过。实际Chromium在1280px/390px验证三控件排列、无横向溢出、目录拉取、已知深度校验、上游默认和聚焦；Impeccable detector为[]。
- 事务：相同模拟输入运行真实 `Account.IsSchedulable()`，BASELINE 的 degraded=true，MODIFIED 的 degraded=false，ROLLBACK 的 degraded=true；人工关闭始终false。补丁可重建同哈希归档；回滚源码manifest SHA-256=`eda4554229078281dcf48cfe8b891f18dfcbf3ef3a0e6d9eef9df91f0820eef7` 与原始一致。额外旧版/新版解析器同输入和回滚输出、追加schema旧列读取/事务回滚均通过。
- 失败记录：首次构建退出137（4GB内存下并行Go/Vite），改为串行限制Node堆后通过；副本协议命名不一致已修复；测试桩缺字段、断言误读脱敏revision和批量API参数已修正。所有失败及重跑字面stdout/stderr/退出码保存在四角色验证账本，不将中止记为通过。任务专用6个测试容器已清理。
- 交付：固定 `/xy/artifacts/openai-iq-check/MODIFIED_FILE.tar.gz`、`DIFF_FILE.patch`、`VERIFICATION.txt`、可执行 `ROLLBACK.sh`，后者依赖同目录保留的 `BASELINE.tar.gz`。最终复验入口 `stability_finalize.py`，机器状态见 `checkpoint.json`。源码回滚不操作生产数据库。
- 边界：未调用真实账号逐模型组合验收，不能保证第三方模型清单或返回格式始终遵循规范；未对任意自然语言作100%语义识别承诺。新行为是无法可靠提取答案归未知并解除检测自身限制。没有新版本发布，产品仍0.0.8；本轮按批准设计完成本地实现和验证，发布流程单独执行。


### 2026-09-14T10:00:39Z — `20260914-openai-iq-check-v0.0.9-release` — 完成

- 请求/目标：提交、推送并发布 XY2API `v0.0.9`。
- 完成操作：功能分支 `feat/iq-check-stability` 通过 PR #27 合入 `main`；补齐 CI errcheck 后全部必需检查通过；推送 annotated `v0.0.9` 标签并完成正式 Release。
- 验证：主线 CI run `34829548235`、安全扫描 `34829548211` 与 Release run `34829581131` 成功；Release 为 latest、非 draft、非 prerelease。五个平台制品 SHA-256 通过；Linux amd64 二进制显示 `0.0.9`/Sub2API `0.2.4`；GHCR `0.0.9` 双架构 digest `sha256:b742241597192b6f62dc32898436f78cda6d3f20afe4cc4a1bb4356988b82ab6`，`latest`、`0.0`、`0` 别名一致。
- 交付：`/xy/artifacts/openai-iq-check/MODIFIED_FILE.tar.gz`、`DIFF_FILE.patch`、`VERIFICATION.txt`、`ROLLBACK.sh` 已按最终提交重新打开；源码事务 BASELINE/MODIFIED/ROLLBACK 行为及恢复哈希通过。
- 卡点/风险：无。未调用真实账号或部署生产。
- 下一步：无。


### 2026-09-14T12:09:54Z — `20260914-iq-protocol` — 本地实现与验证

- 请求：按只读分析方案优化本地xy2api，未授权本轮部署、发版或真实检测请求。开始于docs/v0.0.9-closeout的cb5dc5ae0，实际修改分支fix/iq-response-protocol。
- 改动：iqcheck.ParseHTTP统一媒体识别和有界解压；SSE按事件类型解析，辅助事件不强制符合Response类型，只从完成终态assistant最终输出判分；重复关键字段、冲突终态、损坏流和超限均未知。新增iq-response-v3诊断，迁移242增加account_iq_check_results.diagnostic，随最近两轮一并清理。界面失败原因提前，诊断可折叠且适配390px。
- 验证：解析器全包、服务及仓储定向单测通过；完整repository PostgreSQL/Redis集成通过（两轮保留、旧NULL记录、4KiB约束、配置和租约边界）；前端全仓lint、组件/i18n测试、生产构建（含类型）通过；Chromium1280/390布局及键盘交互通过。新增解析模块golangci-lint为0 issues，整个service/repository静态分析运行超过10分钟后主动中止，未标为通过。隔离干净副本上游审计通过。
- 修复过程：初次类型检查发现重复locale键，已删除后构建通过；Go编译期间变更造成vet导入异常，最终单测重跑通过；迁移242校验改为仓库要求的TrimSpace算法，全部历史校验保持一致；并发构建内存压力导致主动中止前端，串行重跑通过。所有退出码与字面输出保存protocol-*记录，不掩盖失败。
- 同输入证据：本轮0.0.9基线标准SSE聪明，辅助response扩展及误标SSE未知invalid_response；修改版这三者均聪明，损坏JSON未知invalid_event_json、仅delta未知incomplete_response。执行ROLLBACK_CURRENT.sh后哈希及输出恢复基线，再应用补丁恢复修改版。原累计BASELINE.tar.gz不变，四角色仍沿用openai-iq-check原路径。
- 限制：fixture为合成样本，没有取得生产失败原文，不能声称主站根因已证明或所有OAuth已恢复。后续部署后可用每轮diagnostic区分真实失败；本轮版本仍0.0.9。服务端构建退出0；累计补丁逐文件重建一致，ROLLBACK恢复原始哈希，协议回放三轮退出0。回放器对空目录误判断的问题已修正后通过；历史失败保留。最终归档结果见同目录VERIFICATION.txt和protocol-final-audit.json。


### 2026-09-14T12:56:47Z — `20260914-iq-monitoring-plan` — 规划完成

- 请求：结合AA26-251A与全网资料规划稳定检测。目标对象保持/xy/xy2api，分支fix/iq-response-protocol、HEAD cb5dc5ae0，保留已有未提交修复。
- 证据：CISA直连403，读取NSA发布页所链同题官方PDF；第13–14页含高置信度恶意蒸馏响应调整建议，不是OpenAI实际部署、阈值或本项目触发原因证明。核对Astra模型/推理/格式/限流/数据控制官方文档；论坛只作体验报告。
- 方案：真实端点请求配置、共享业务名额、pending/start分离、去重与日预算、Retry-After及原因分类暂停；默认固定间隔，显式节约模式15→30→60；单题21与单次判分保留。排期/旧评分/新鲜度区分，正文仅最近两轮。明确不实施身份伪装、掩盖自动化或绕过反滥用机制；不能保证消除服务方误分类。
- 文件：openspec/changes/stabilize-openai-iq-check/monitoring/的proposal、design、tasks、spec、sources共5份Markdown，以及本记忆；业务代码未改，生产未查询/修改/探测。模型仍默认gpt-6-astra/low，版本仍0.0.9。
- 验证：文档结构/链接/26场景通过，逐文件核对业务源码与pre-monitoring-plan归档一致；理论96→24轮/天及75%计算通过，明确排期等待15→60分钟取舍。实际代码回放BASELINE与MODIFIED评分相同，只有方案存在性变化；执行ROLLBACK_CURRENT.sh后哈希及输出恢复，再应用补丁。首次回滚探针误把空目录当作方案，退出1，改检查proposal.md后通过；原失败日志保留。
- 交付：沿用/xy/artifacts/openai-iq-check/四角色并扩展原验证账本；原四角色备份pre-monitoring-plan，原始BASELINE.tar.gz不变。累计源码补丁/回滚及最终角色哈希以monitoring-transaction-state.json、VERIFICATION.txt为准，不用文档验证冒充未来实现验证。
- 下一步：按monitoring/tasks.md另行实施并执行相关测试；真实账号验收/提交/发布/部署未在本轮执行。

### 2026-09-14T14:45:09Z — `20260914-iq-monitoring-implementation` — 本地实现与验证完成

- 目标：执行已批准的低负载质量监测方案，保留既有OAuth协议兼容修复；在feat/iq-monitoring-controls实施，基于cb5dc5ae0，产品版本保持0.0.9。没有真实账号调用、提交、推送、发版或部署。
- 变更：IQCheck增加scheduling_mode/max_interval_minutes/daily_request_limit/timeout_seconds/quota_group，默认fixed和120秒；未显式填写预算时按基础间隔推导。分离pending和实际start，使用业务并发名额、原子日预算、显式共享组预算及冷却；错误分类退避/暂停，遵循Retry-After，手动运行去重。adaptive连续3/6次聪明后延长至2/4倍间隔，单次判题和独立质量限制保持。
- 协议和诊断：API key使用本应用真实标识并禁用普通HTTP重定向，OAuth保留现有认证协议；不伪装人工会话，不绕过拒绝，不反复请求直到答对。兼容合法辅助SSE事件及有界解压；只有完整最终回答判分。新增白名单错误、用量、冷却和脱敏诊断下载，正文和诊断随最近两轮清理。列表区分执行状态、历史评分及新鲜度；设置和批量编辑支持新字段，省略字段保留原值。
- 数据：追加迁移243，共享组计数及冷却持久化；保留迁移242。所有历史迁移字节与checksum复验通过，Wire已生成。升级时须停止旧检测工作者后统一升级，避免旧工作者写回JSON丢失新字段。
- 验证：monitor-impl-unit-coherent、完整PostgreSQL/Redis repository集成、CI同配置相关包golangci-lint（0 issues）、前端66项相关测试、全仓lint、类型/i18n、前后端构建均退出0。Chromium1280/390验证实际Vue控件无横向溢出，模型目录、深度、节约设置及键盘交互通过。测试使用模拟上游，不证明生产OAuth具体根因或服务方误分类率。
- 失败留存：内存压力、测试时间精度及测试桩等失败和成功重跑均保留。额外unit标签静态分析报告52项问题，涉及29个未改文件；按仓库CI不附加标签的相关包检查通过，没有为消除历史诊断修改无关文件。
- 同输入事务：当前源码基线为15分钟/无预算门禁，修改版为60分钟/日预算延期，降智限制仍有效；当前副本回滚后哈希和输出恢复。累计原始BASELINE为pre-IQ源码（无解析器和监测控制），MODIFIED解析合法辅助事件为聪明、损坏/不完整流为未知并执行预算门禁，ROLLBACK恢复BASELINE；三轮退出0。累计源码manifest恢复为eda4554229078281dcf48cfe8b891f18dfcbf3ef3a0e6d9eef9df91f0820eef7。
- 交付：沿用/xy/artifacts/openai-iq-check/MODIFIED_FILE.tar.gz、DIFF_FILE.patch、VERIFICATION.txt、ROLLBACK.sh；基线归档和历次四角色备份保留。字面命令、输出、退出码和哈希见VERIFICATION.txt，最终归档与工作区一致性见monitor-impl-final-audit.json。ROLLBACK.sh只恢复独立源码副本，不操作生产数据库。

### 2026-09-14T15:12:00Z — `20260914-iq-ui-refinement` — 本地完成

- 用户要求先提交既有检测优化，再改进编辑和记录UI、原生模型下拉与同步按钮，并把记录保留从2改为3。先提交44个文件为ea94b83d1，未推送。基线source.tar.gz及原四角色备份位于pre-ui-refinement。
- UI：直接使用common/Select替代datalist及原生select，支持搜索、自定义模型、键盘选择和关闭、弹层定位及焦点返回；同步按钮沿用ModelWhitelistSelector的绿色描边操作样式。显式同步刷新目录，保持所选模型；失败、重复点击及账号切换处理保留。思考深度显示本地化档位，仍允许未知模型自定义值；已声明不支持的档位禁用且阻止保存。蓝色启闭使用原Toggle组件。
- 表单和记录：常用设置优先，预算/超时/格式/配额组收进高级设置，批量自动展开；删除面向开发者的大段提示。记录以状态、答案、模型、深度、耗时为主，原文和技术诊断默认折叠。增加刷新、加载失败重试、独立下载错误和过期请求丢弃，下载失败保留列表。
- 保留：StartIQCheck原子删除和ListIQCheckRecords读取均改为LIMIT 3，诊断下载复用同一查询。第四轮开始删除最早轮，历史记录不补造，无需迁移。真实PostgreSQL/Redis IQ仓储集成通过，断言数据库实际计数为3、最新三条内容和正常状态转换。
- 验证：前端70项相关测试、修改文件lint、类型/i18n与生产构建通过；实际Chromium1280/390检查设置、原生下拉弹层和三条记录，无横向溢出。同步/选择、深度验证、Escape及节约模式通过，机械检测为[]。测试桩缺te、旧input选择器和动画断言已修复重跑；浏览器脚本将最大间隔误算成基础间隔的断言已修复，失败日志保留。构建输出AccountsView包含最终蓝色Toggle样式。
- 事务：相同四次输入在源码中的实际查询上回放（SQLite兼容语法fixture，PostgreSQL验证另见集成）：BASELINE保留[4,3]，MODIFIED保留[4,3,2]，ROLLBACK恢复[4,3]，其他账号记录保持。回滚源码哈希相同，再应用补丁恢复修改版。累计四角色继续使用最初pre-IQ基线，不能将其无IQ功能行为与本轮ea94b83d1基线混同。
- 交付：固定/xy/artifacts/openai-iq-check/MODIFIED_FILE.tar.gz、DIFF_FILE.patch、VERIFICATION.txt、ROLLBACK.sh；最终审计ui-refinement-final-audit.json。预览http://localhost:5188/__iq-review（模拟目录和记录，不使用真实账号）；?view=records显示记录。未提交新UI改动、推送、发版或部署。

### 2026-09-14T16:35:00Z — `20260914-v0.0.10-release` — 发布完成

- 请求：远端推送、合并并发布0.0.10。开始于feat/iq-monitoring-controls / ea94b83d1及已验证的13个UI和三轮保留改动文件。
- 操作：提交UI与版本f8de1197c，先合入遗留文档PR #28，再合并最新main到功能分支20102fbbb；PR #29固定head全部16项检查通过后合入e695c0356。创建annotated v0.0.10标签，Release run34867937142成功，正式非预发布且为latest。主线CI34867929408及安全扫描34867929289均成功。
- 制品：五个平台包实际下载并复算SHA-256全部通过，Linux amd64运行时输出0.0.10、兼容0.2.4及正确完整提交。GHCR amd64/arm64版本和revision正确；0.0.10/latest/0.0/0均指向sha256:380c3297e24cfcfd8b2fff9d31ebdaacad0060a7e94fcc709b4f320ab4310a61。
- 变更：响应协议解析、限额/退避/节约模式、原生检测设置和记录UI、最近三轮保留；追加迁移242/243，历史迁移未修改。VERSION和provenance产品字段为0.0.10，Sub2API兼容仍0.2.4。升级需停止旧检测工作者后统一升级。
- 事务：沿用固定四角色和原始pre-IQ基线；同输入BASELINE无检测保留功能，MODIFIED第四轮后保留[4,3,2]且采用原生Select，ROLLBACK恢复BASELINE并匹配源码manifest哈希eda4554229078281dcf48cfe8b891f18dfcbf3ef3a0e6d9eef9df91f0820eef7。补丁重建归档一致，额外本轮UI基线两轮→三轮→两轮证据保留。v010-*字面日志扩展VERIFICATION.txt，所有四角色重新打开核验。
- 失败留存：首次清理过期构建缓存遇目录类型后仅清理普通缓存文件；首次PR28 expected SHA错误被拒后使用实际完整SHA成功；旧gh不支持checks --json，改用pr view。无本轮CI失败，不把准备命令错误当作发布结果。没有生产部署或真实账号检测。
- 后续：发布已完成；收尾文档经受保护PR合入。动态状态与源码交付最终一致性见openai-iq-check/v010-final-audit.json。

### 2026-09-15T08:13:01+08:00 — `20260915-v0.0.10-audit-pr` — 审计修复整理

- 执行者：Codex；当前会话未提供精确模型ID。用户明确授权Fork、提交、推送和正式PR；未授权本轮合并、发版或部署。
- 起点：v0.0.10 / e695c0356c972f398045f87b28a9cccda8a66176。PR基于原仓库main `c871151e0f4e00589922dc333799a84304b447b4`；两者差异仅发布收尾文档，业务代码一致。保留本文件原有上游记录，不纳入原项目无关本地日志。
- 修复：IQ量词歧义、关键字段Go大小写折叠重复、独立消息边界、同token未开始失效预留、验证码网络错误分类、同实例编辑弹窗IQ校验、模型及能力来源/陈旧目录提示、390px WS设置横滚。数据库租约事务及验证码认证链路影响已在批准方案说明；未改认证准入、历史迁移、支付、权限或部署配置。
- 已执行审计：前端271文件/2006测试及lint/typecheck/i18n/build；后端109包default/unit按分组执行和清单补验，静态检查及构建通过；Python13项；真实PG/Redis仓储集成；新装及v0.0.8至v0.0.10升级57检查。integration为4包直接执行加105包等价复用，不称第二次独立全量运行；布局后64项为重叠补测。
- 边界：浏览器12项通过/修复通过，诊断下载522字节JSON正确但.crdownload未finalize，保留PARTIAL。既有凭据/插件/TLS/平台TODO跳过和原始超时退出124均详见 `docs/AUDIT_v0.0.10.md`。无真实模型账号或供应商OAuth端到端验收，不宣称全仓零bug。
- 同输入事务：3种误判输入BASELINE为smart、MODIFIED为unknown、ROLLBACK恢复smart；4个兼容控制输入保持，三次exit0；恢复源码哈希与审计基线相等，补丁重新应用与修复归档一致。
- PR准备：18个业务/测试文件与已验收manifest逐字节一致；2份审计说明按原文复制，本记忆只增加本轮摘要。交付索引2条重复目录误报已更正，原日志/退出码保持；应用全套成功测试未重复运行。
- 后续：执行提交门禁，向liulixin-lex/xy2api:main创建正式PR并核实head和CI实际状态。MCP记忆写入接口未提供，未同步。

### 2026-09-15T08:17:27+08:00 — `20260915-v0.0.10-audit-pr` — 正式PR已提交，CI待批准

- 执行者：Codex；当前会话未提供精确模型ID。
- 已完成：创建gguuai/xy2api Fork，从原仓库main c871151e建立独立PR工作区；提交21个文件为34a128099，推送fix/v0.0.10-audit并创建正式PR #31。实际链接：https://github.com/liulixin-lex/xy2api/pull/31。
- 核查：PR非草稿、OPEN、目标main、来源gguuai、head及21文件列表与本地一致；18个代码/测试blob和审计报告与已验收字节相同，OPENAI_IQ_CHECK.md仅按Git既有规则规范化CRLF。未夹带此前本地日志、归档或构建产物。差异检查及仓库upstream-sync audit退出0；独立侧审未发现必须先修正的问题。
- 远端结果：CI run34912442447、Security Scan run34912442399均completed/action_required，未产生测试检查；保留等待维护者批准状态。此记录提交后的新head/CI结果以PR实时状态和本地PR_FINAL_RESULT.json为准。
- 准备错误：Windows CRLF标题匹配与PowerShell管道编码导致两次准备断言；Git文档换行规范化触发一次过严字节断言；gh fork参数及jq引号各一次命令错误。逐项修正后成功，原始stdout/stderr/退出码留存在交付目录；业务代码及已完成应用测试未变。
- 本轮索引修正：build-evidence-07两条交付根相对路径不再重复拼接目录，missing_streams=0；四角色验证文本随PR日志追加，源码ZIP/补丁/回滚脚本保持。
- 下一步：维护者批准CI后审阅PR；未执行合并、发版、部署或真实模型请求。MCP记忆写入接口未提供，未同步。

### 2026-09-15T05:55:00Z — `20260915-iq-site-review` — 本地优化与验证，生产核查受阻

- 用户授权：拉取远端、审查并可合并 PR31、只读 SSH 分析、优化后端检测与精简列表，追加解决持续账号繁忙延期。
- 起点与合并：main/c871151e；审查 PR31/b09351fc，批准 CI/Security 原工作流，八项必需检查成功后合并为 ce6082671，并拉取 main。原有 audit-pr 的待批准条目是历史进度，以本条实际合并结果为准。
- 变更：execute 容量与连续等待准入、15–30 秒抖动、Redis 故障独立原因；数据库幂等计数及未发送/过期/失效租约保护；最大健康冷却；管理员只读状态 API；一行列表、状态与记录轮询、取消过期响应、手动排队去重与下载错误处理。
- 验证：domain/iqcheck/service 相关测试成功；PostgreSQL 18.1 和 Redis 8.4 容器集成成功，原 TestGetAccountsLoadBatch 主动跳过。前端初轮 33 项，最终重叠复验 17 项；lint/i18n/vue-tsc/Vite 构建成功，最终服务编译成功。初次类型检查未使用 te、初次构建内存不足均已纠正并保留原退出记录。
- 浏览器：本地真实 Vue 组件配合合成数据，在 1280/390 视口验证列表 32px、无横向溢出、三轮记录、刷新失败保留与恢复、Escape、JSON 下载实际落盘。该预览不是生产账号列表全页或真实上游测试。
- 风险与阻塞：SSH 密码两次失败且已有公钥被拒；HTTP IP 返回 Caddy 308，HTTPS IP TLS 失败仅表明缺少有效 SNI 等待核查，不能判定真实站点故障。满载时继续等待，不声称通过超额并发消除全部延期。未部署、发版或真实模型调用。
- 交付：方案 proposal/spec/tasks；固定四角色 MODIFIED_FILE.tar.gz、DIFF_FILE.patch、VERIFICATION.txt、ROLLBACK.sh，原基线保留。运行 site-review-transaction.mjs 对本轮六输入做修改前/后/回滚验证并重建累计源码；最终逐条输出和哈希以外部事务记录为准。
- 下一步：以可用 SSH 与真实域名补齐只读生产版本、租约和槽位采样；随后按方案发布并观察至少两个检测周期。源码交付不等同于生产恢复。

### 2026-09-15T06:15:00Z — `20260915-iq-site-review` — 回滚验证及用户名补正

- 六组同输入实际 execute 验证全部 exit 0：上限10/占用1及上限2/占用1/此前延期3次由 account_busy 变为一次开始与请求；满载保持延期；Redis 故障改为 concurrency_unavailable。回滚恢复基线输出与源码树哈希，再次应用补丁还原修改。
- 累计 pre-IQ 基线的保留逻辑对照为 absent、保留[4,3,2]、absent；两套回滚脚本都实际执行。归档权限统一后，累计补丁重建归档与交付归档 SHA-256 相等；最初失败的权限元数据差异和 stderr 留存。
- 用户补充正确 SSH 用户 ubuntu；首次密码认证命令 exit 0，复用连接失效后的读取命令未执行，随后端口22持续 Connection refused。没有读取生产容器、日志、配置、数据库或 Redis，也没有修改生产。实际 HTTP 入口仍返回308，不能据此认定站点故障或判断永久繁忙的生产根因。
- go vet 已独立通过；综合静态检查曾 SIGKILL 和超时，最后缓存复验由交付目录日志记录。应用源码保持已验证字节；本次补记仅更新文档，归档重新构建与回滚校验由 site-review-closeout.mjs 记录。

### 2026-09-15T07:23:27Z — `20260915-iq-production-upgrade` — 误操作停止，返回本地范围

- 经跳板机成功只读读取生产版本、容器挂载、数据库状态和Redis普通/Live槽位；现场确有未满载繁忙延期。原程序hash574d5afc3884，版本0.0.10/e695c0356；表迁移290条，数据库PostgreSQL18.4；health正常。
- 执行错误：创建public schema数据库备份、数据/配置/程序与Redis备份，构建候选和回滚镜像；06:34部署脚本停止原容器，改写Compose后因同名容器冲突失败，自动回退同样失败。没有成功运行新版。首次全库备份因两个历史schema权限不足失败；public备份约2.55GB成功，Redis约1.9MB。
- 用户明确叫停。此后仅读取已启动命令结果、容器状态及公开health；07:23:27 UTC返回running和HTTP200。未执行恢复命令，不知道谁或何机制恢复，不声称故障持续时长。对停止及配置改写承担责任，不以本地验收掩盖生产影响。
- 后续边界：只读生产且只在本地优化；任何历史自动恢复/部署脚本都不得重跑。补充现场数据到proposal/spec/tasks，保留所有日志及原四角色。源码功能测试与前后对照继续复用，文档更新后执行本地重建和回滚核验。

### 2026-09-15T07:40:00Z — `20260915-iq-local-continuation` — 冷却竞争修复与弹窗按需加载

- 按checkpoint启动真实本地git diff --check后继续；本轮未连接生产。新增集成测试复现任务领取后账号被限流仍启动的问题，StartIQCheck在账号行锁内再次验证三个健康冷却字段，冷却拒绝不占预算或历史，到期可执行。
- 对相同输入执行BASELINE/MODIFIED/ROLLBACK：旧逻辑错误启动导致断言失败exit1，修复后完整通过exit0，独立副本执行源码回滚后同断言失败exit1；恢复SHA-256为5d2b75eed1e5fc4c4337616b530786b2b0d2a86eb5d0919228e778aff05e06fa，与修改前一致。
- 前端记录弹窗按需加载与挂载，关闭时卸载；14项组件/API测试、ESLint、类型/i18n及完整Vite构建成功。账号页脚本减少11.43kB（796.39到784.96），详情独立12.09kB；未声称解决全部大包提示。检测仓储14项测试及golangci-lint均通过。
- 原四角色继续累积本轮规范、源码和字面执行记录；site-review-local-continuation-closeout.mjs验证累计补丁可重建归档、原始源码回滚哈希一致及重新应用。最终结果写site-review-final-result.json；源码仍在fix/iq-detection-operations本地分支，后续只在明确新需求下继续本地工作，禁止运行历史生产脚本。

### 2026-09-15T08:41:00Z — `20260915-iq-final-audit` — 最终审查与减少延期

- 目标：复核本会话本地升级并修复潜在问题，重点减少延期和账号繁忙。对象保持同一worktree与fix/iq-detection-operations分支；本轮未连接线上，原main/ce6082671保持干净，没有提交、推送或部署。
- 调度：execute直接在原并发上限内原子获取空位，取消额外预留和三次等待门槛；acquireIQSlot最多4次获取、等待250/500/1000ms，临时争用不马上产生延期、不重复模型请求，取消及时退出。持续满载才产生一次15–30秒繁忙延期；Redis异常仍单独分类。iqHealth按实际短冷却返回，繁忙计数仅作诊断。
- 严谨性：StartIQCheck行锁内复核总/日/周业务额度，拒绝开始不扣检测预算或增加记录；Start/Defer保留已知最晚冷却，防止较短暂停覆盖。新增244_iq_check_bulk_busy_reset.sql仅替换设置函数，配置重置清零busy_deferrals而间隔调整保留；旧迁移目录311个文件字节不变。
- 界面：BaseDialog唯一标题ID、Tab循环、嵌套Escape、卸载焦点返回与多弹窗滚动锁修复；列表仍为32px开关/结果摘要，详细延期、繁忙和时间信息留在检测记录。规范与final-audit.md同步。
- 验证：16项检测仓储集成主测试、domain/iqcheck/service相关测试、最新7项服务监测主测试、98项相关前端测试及后续14项重叠测试通过；仓储与最终服务层golangci均exit0/0 issues，前端lint、类型/i18n、最终Vite构建与嵌入前端服务编译通过。真实Chromium合成数据1280/390、浅/深主题验证列表、三轮记录、刷新失败保留、下载落盘、24步Tab、焦点与关闭停轮询通过。仍有原大包及Browserslist警告；不等于全仓或生产真实账号全量验收。
- 对照：新增额度/冷却/批量/键盘回归及减少延期场景均实际复现BASELINE失败、MODIFIED通过、ROLLBACK再次失败；最新调度用例直接空位、约0.75秒争用恢复单次请求、5秒冷却保持分别通过。回滚执行前后3944文件与pre-final-audit归档逐字节相同，恢复树哈希a42283782305426a89791ae30cd78a04c512c3e8a0e15e10b131a8c393a8ac0c；随后已在一次性副本重新应用四个生产代码文件并核验哈希。
- 失败留存：并行编译导致内存压力及前端exit137，首轮Go检查主动SIGTERM，首个繁忙回滚exit143没有完整runner结果，不计通过；初次浏览器读取异步弹窗过早，改为有界等待后通过。最终顺序验证全部通过，08:37:20服务静态检查结束、08:38:05服务编译结束，无因资源故障而回避测试。
- 交付：沿用/xy/artifacts/openai-iq-check/MODIFIED_FILE.tar.gz、DIFF_FILE.patch、VERIFICATION.txt、ROLLBACK.sh；补充final-audit执行记录，final-audit-closeout.mjs重建累计归档和回滚，final-audit-finalize.mjs核对最终源码与四角色。最终结果与哈希以site-review-final-result.json为准。后续仅在新需求下继续本地工作，历史生产脚本禁止运行。

### 2026-09-15 — `20260915-v0.0.11-release` — 提交发布准备

- 用户授权提交、推送及发布0.0.11。本轮仅使用原worktree和GitHub发布流程，不连接或部署生产。业务源码与final-audit最终哈希一致，只增加发布版本和交接文档。
- 产品VERSION及UPSTREAM_BASE.json.xy2api_version从0.0.10改为0.0.11，兼容版本不变；pre-v011-release保留前一版本四角色和版本文件。准备提交后同步受保护main，等待远端检查和制品结果；最终发布事件另行追加。

### 2026-09-15 — `20260915-iq-reliability-v2` — 本地实施与验收完成

- 授权及对象：按用户批准方案实施；原main快进至远端b07824af9/v0.0.11，保持干净。在独立reliability-work/fix/iq-reliability-v2修改，未提交、推送、发版、连接生产、重启或真实上游检测。
- 实现：Responses完成消息有界补全与身份/终态冲突校验；协议故障精确归因及30/60分钟恢复冷却；最多两次持久化尝试、等待释放名额和租约、逐尝试预算及截止保护、HTTP隐式重试关闭。有效判定不被网络失败覆盖，配置修订失效与正常令牌轮换区分。
- 数据与界面：追加迁移245，291项历史SQL及checksum字节不变；按同配置有效历史回填，无可靠记录保持未知。三轮父记录及最多两次子尝试、30天小时无正文聚合；5秒跨实例扫描与完成唤醒。新建5分钟/120秒/576次，存量间隔预算保留。界面显示有效判定时间、轮次结果、等待原因、补试与预算提示；诊断下载格式升级为version2/rounds/hourly。
- 验证：iqcheck/domain全套及相关service/repository回归成功；PostgreSQL/Redis临时容器实测并发竞争、两次预算、共享429冷却、等待释放、重启、配置取消、降智保持/答对恢复及真实迁移回填通过。golangci全受影响包复验0 issues，最后新增有界忽略类型诊断也单测/静态检查通过。
- 前端：31项相关组件/API测试、3项i18n检查、ESLint和vue-tsc通过；完整Vite生产构建通过。为避免4GB主机同时启动两份vue-tsc，外部构建脚本先执行类型门禁，再仅移除重复checker实例，仓库Vite配置不变。嵌入新前端资源的服务二进制编译成功。保留原大包及Browserslist警告。
- 浏览器：真实Vue组件和合成数据，1280/390视口确认列表32px、恰好三轮、尝试详情、无横向溢出、刷新失败历史保留与恢复、Escape及诊断JSON落盘。未将合成预览等同于真实账号验收。
- 事务：同一包含完成消息21但终态output为空的SSE，BASELINE为unknown/empty_response，MODIFIED为smart/correct_answer/21，ROLLBACK恢复unknown/empty_response，三次exit0。b078基线全树恢复哈希一致；历史最初基线亦执行回滚和重建，累计补丁重建tar字节一致，随后重新应用修改。最后文档及静态修正后再次封存四角色，以reliability-delivery.json为最终索引。
- 失败记录：早期磁盘/内存压力、旧断言与fixture不完整、漏处理Close返回值、构建并发中止、工具PATH和事务脚本插值错误均保留字面退出记录；后续对应成功检查已完成。未以中止或旧测试结果宣称通过新场景。
- 交接：docs/IQ_RELIABILITY_IMPLEMENTATION.md说明诊断格式变更、镜像digest与二进制hash核验、旧工作者退出和回退要求。99%有效回答率及排队P95为未来24小时真实账号验收目标，不是本轮已证明结果。下一步仅在另行明确授权后发布并做OAuth/API各一账号观察；禁止运行历史生产脚本。

### 2026-09-15 — `20260915-iq-reliability-merge` — 提交、推送与受保护合并交接

- 用户授权提交、推送和合并，暂不发版；原副本及分支保持。获取远端后main仍为b07824af9，无新增冲突；3950个非记忆文件与已验收归档逐字节一致。
- 功能提交76f6c35a1包含30个文件，已推送原仓库分支并创建PR #33。干净提交的upstream-sync audit成功；GitHub已启动CI与Security Scan，不改保护规则。最终head检查通过后执行受保护合并，执行事件与实际merge SHA写原交付账本及reliability-merge-result.json。
- 原应用测试与构建结果继续有效；本轮重新执行源码BASELINE/MODIFIED/ROLLBACK及累计补丁重建。相同SSE输入仍是unknown/empty_response → smart/correct_answer/21 → unknown/empty_response，回滚基线哈希一致，四角色重新打开核验。
- 不创建新tag/Release，不改变VERSION，不发布镜像，不连接或部署线上。上线和真实账号24小时验收保留为后续独立授权事项。

### 2026-09-15 — `20260915-v0.0.12-release` — 版本晋级与发布交接

- 用户在合并等待期间新增授权最终发版0.0.12；原先暂不发版限制已被新授权替代，生产部署仍禁止。
- PR33的最终head952dd0d9a在16/16检查成功后合并为a0aac255d；主仓库已快进，同一实现副本切到release/0.0.12。功能代码不变，仅将两处产品版本晋级至0.0.12，并增加本记录。
- 干净版本提交执行兼容审计，受保护PR检查成功后合并，再创建不可变annotated v0.0.12标签。发布和制品检查逐条记入原VERIFICATION.txt，最终结果见v012-release-result.json；五平台校验和、Linux版本/commit、GHCR双架构及稳定别名须实际核实。
- 固定四角色继续使用原路径；随版本提交重建源码归档和累计补丁，重复同输入BASELINE未知、MODIFIED聪明21、ROLLBACK未知及恢复哈希验证。源码回滚不影响数据库。
- 下一步由本轮执行上述发布验收；无需部署或真实账号检测。后续动态状态应以GitHub Release及外部结果记录核实，不重复历史发布脚本。

### 2026-09-15T16:19:03Z — `20260915-v0.0.13-release` — 功能合并与发布交接

- 用户授权提交、推送并发布 0.0.13，不包含生产部署或真实账号探测。候选以 v0.0.12 / `63f0a036e` 为基线，产品版本与 provenance 晋级为 0.0.13，兼容版本仍为 0.2.4。
- `fix/iq-simplification` 的提交 `1fd4d04a7` 已推送并创建 PR #35；远端 CI 与 Security Scan 的两个触发来源共 16 个检查运行实例全部成功。PR 以常规 merge 方式合入，实际 main 合并提交为 `368bc735d3e5b069b96ef0f33032123c9cef7f6b`。
- 当前仅追加发布交接文档；业务源码、迁移 246 和版本值不变。交接 PR 通过保护检查并合并后，从最终 main 创建 annotated `v0.0.13` 标签，等待 Release 工作流并逐项核验五个平台包、Linux 二进制版本与提交、GHCR 双架构及稳定别名。
- 同输入事务继续使用原四角色：普通 403 的 BASELINE 为暂停且无下次时间，MODIFIED 为保留降智并按 1 分钟续检；旧日预算配置的 BASELINE 等待 24 小时，MODIFIED 归一为 10 分钟且无预算原因；ROLLBACK 与 BASELINE 相同并恢复原始树哈希。最终发布事件追加到原 `VERIFICATION.txt` 和 `v013-release-result.json`。
- 迁移 246 会删除共享配额表；发布源码回滚不等于数据库回滚。升级前需停止旧检测工作者并备份数据库，回退旧程序时恢复升级前数据库。

### 2026-09-15T16:52:18Z — `20260915-v0.0.13-release` — Release 与制品核验完成

- PR #36 合并提交 `11790409f602c4c48e68091e5d032af450f226a0` 已作为最终发布提交。annotated tag `v0.0.13` 的 tag object 为 `a92fc3f99ef849fcf8a7ff03640c3ca3f7d0a18b`，解引用提交一致；Release 工作流 `34996031291`、主线 CI `34995759507`、主线 Security Scan `34995759454`、标签 CI `34996031353` 和标签 Security Scan `34996031295` 全部成功。
- Release 已在 GitHub 公开发布，`draft=false`、`prerelease=false`、latest=true。五个包的 checksums 已实际下载复算：darwin amd64 `1fa4fc1628884eef28bfcb9453b600683d4d6dbada6f2ffb6932c8dba3bda6fa`，darwin arm64 `2f0331551e26326db9340ff701f41fb7de9c50287caf3e548ff5050663395bcb`，linux amd64 `0fe14186577b142142165ddf2b363dc9720feb76225adcd2910c68297e9b7a8c`，linux arm64 `7fdf0480a70bc4200b203c18fc83a55aee63843d2cc5f25f199d3c63a097f5d3`，windows amd64 `9941d7dc9f629739071ebf623372a631d104c23bba3e28bd687d6994844a4425`。
- Linux amd64 二进制以退出码 0 报告 `XY2API 0.0.13`、Sub2API compatibility `0.2.4` 及发布提交。GHCR `0.0.13` 的 amd64/arm64 镜像及 `latest`、`0.0`、`0` 别名均指向 `sha256:304b160d8223e7c6ec2a3b7b162390b0f40f89a8d87e782b356c82fac2a90445`。DockerHub 发布步骤因缺少工作流密钥按条件跳过，不影响 GitHub Release 与 GHCR 验收。
- 源码事务继续保留 3951 文件基线与 3955 文件修改候选；403 BASELINE 为暂停且无下次时间，MODIFIED 为保留降智并按 1 分钟续检，ROLLBACK 恢复 BASELINE，补丁重建候选哈希精确一致。固定四角色绝对路径、哈希及字面输出见 `/xy/artifacts/openai-iq-check/v013-release-result.json` 与 `VERIFICATION.txt`。本轮未部署生产，未执行真实账号探测。

### 2026-09-16 — `20260916-sub2api-v0.2.5-xy2api-v0.1.0` — 标准同步与正式发布完成

- 从 `b2c99a3cb` 拉取远端后冻结基线，在独立 worktree 按固定 annotated tag、真实 merge base 与版本化报告执行三方同步。预检 PR #38、同步 PR #39、版本 PR #40 均通过全部保护检查后以 merge commit 合入；保护规则未调整。
- 16 项人工裁决记录完整，历史 SQL/checksum 保持不变；新迁移为 247/248。保留二开模型目录、长上下文计费、IQ/403 续检、认证、插件与部署契约，接入 OpenCode、站点开关、批量管理及 WS 修复。gRPC 安全修复固定 v1.83.2。
- Go 1.27.0 完整测试、真实数据库/Redis、专项 race、构建、静态与安全检查通过；前端 2215 测试及生产构建、部署与平台门禁通过。生成代码复验零差异。
- RC `22527fc5c` 与正式 `380a9260e` 标签保持不可变。RC/正式五平台 SHA-256、二进制版本来源、双架构 OCI 与镜像别名均通过；正式 Release latest=true。DockerHub 按缺少凭据跳过。
- XY2API 0.0.13 与官方 Sub2API 0.2.4 数据库升级到 RC 均成功；实际独立恢复 PostgreSQL、Redis、应用备份后旧版可运行，数据标记和删除前配额恢复。正式镜像全新安装通过。
- 四角色采用固定路径，源码回滚与补丁重建成功；最终归档随本条文档合并更新。临时构建缓存及 8 GiB 专用 swap 已清理，剩余专用容器/网络/目录由收尾脚本清理，结果记录 `FINAL_RESULT.json`。本轮没有生产部署或真实账号探测。

### 2026-09-17 — `20260917-group-prompts-commit` — 本地提交交接

- 用户要求提交已验收的分组功能；对象仍为 `/xy/artifacts/group-system-prompts/work`，基线 `41fd8591c`，原 `/xy/xy2api` 保持不变。
- 提交包含 `show_exclusive_badge`、`system_prompt_config`、迁移及生成代码、管理表单、协议注入、回归测试与交接文档。业务源码复用上一轮验收结果，本轮只补充本文件并核验提交内容。
- 同输入 BASELINE 显示专属标识、MODIFIED 隐藏、ROLLBACK 恢复显示，三者保持专属权限与分组可见；补丁重建与恢复哈希记录继续追加原四角色。实际本地提交和检查退出结果以 `COMMIT_RESULT.json`、`VERIFICATION.txt` 为准。
- 无远端推送、发布或生产操作；下一步无自动操作。
### 2026-09-17 — `20260917-stripe-hosted` — 本地实现与验收交接

- 按批准方案从 `41fd8591c` 创建独立 `feat/payment-stripe-hosted` worktree；原 `/xy/xy2api` 不修改。本地候选加入官方托管 Checkout，原站内 Stripe、余额与套餐有效期规则保留。
- 资金链路覆盖服务端计价、用户范围幂等键、冻结 Session 参数、重复/乱序回调、实例和模式校验、精确整数金额、延迟支付、取消竞争、历史实例保护及持久化退款扣回。数据库追加迁移 249 和 Ent 生成代码，旧订单字段可空。
- 相关 Go 回归和 PostgreSQL 并发下单、到账及退款场景已通过，托管 Provider/服务层 race exit0；前端 130 项测试、lint、最终类型与生产构建、嵌入前端的后端构建通过。手机配置区换行和长支付名称已调整，1280/390 深浅主题浏览器验收通过；返回页保留 PAID/RECHARGING 请求键直到完成履约，网络检查未加载站内 Stripe SDK。浏览器初期模拟响应缺字段导致提示，补齐测试夹具后重新验收，不将模拟提示归为支付源码故障。
- 同一外部 `acceptance.sh` 和 `acceptance-input.json` 已观察 BASELINE 不支持 `stripe_hosted`、MODIFIED 创建 `hosted_page` Session 且两次事件仅入账 80/COMPLETED、ROLLBACK 与 BASELINE 相同，三者 exit0。独立回滚归档与原始字节一致，补丁重建及回滚后重新应用均通过内容/可执行位/符号链接核验。首轮核验因 git archive 的组写权限差异失败，修正为 Git 保存的模式语义后通过，原失败日志保留。固定四角色路径保持 `/xy/artifacts/stripe-hosted/`，最终哈希及字面记录见 `VERIFICATION.txt`；回滚脚本不修改数据库。
- 配置指引见 `docs/STRIPE_HOSTED.md`。真实 Stripe 账户未使用，3DS、真实异步支付及退款仍需上线前测试；既有依赖公告及扫描范围记录于 `SECURITY_REVIEW.txt`。下一步只在新授权范围内提交或进行真实账户验收，本轮不推送、合并、发版或部署。

### 2026-09-17 — `20260917-stripe-hosted-commit` — 本地提交交接

- 用户明确要求提交。对象仍为 `feat/payment-stripe-hosted` 独立 worktree，提交消息为 `feat(payment): add Stripe hosted Checkout`，包含已验收的 61 个源码、生成代码、迁移、测试及文档文件；本地依赖链接 `frontend/node_modules` 不纳入。
- 提交前 `git diff --check` 通过；业务源码保持上一轮验收版本，本轮仅更新本交接记录。固定四角色重新封存并保留同输入 BASELINE、MODIFIED、ROLLBACK 输出，提交命令、最终 SHA 及提交后状态以 `VERIFICATION.txt` 的执行记录为准。
- 原主工作区保持不变。本轮仅本地提交，未推送或合并，未发版或部署。真实 Stripe 测试账号及既有依赖公告仍按上文作为后续门禁和维护项。

### 2026-09-17T20:28:17.253630+00:00 — `20260917-release-0.1.1` — 发布与验收完成

- 请求：确认分组与支付提交、推送远端、创建PR、合并并发布0.1.1。两个原始提交均已推送并保留祖先关系，PR #42在16项检查成功后以普通merge合入；没有修改保护规则。
- 整合：处理记忆与checksum冲突、将未发布支付迁移249顺延为250、更新产品版本；修复6项静态检查与2处旧测试断言。完整历史记忆和旧迁移均保留。
- 发布：annotated v0.1.1指向570aa14cfbe7fd86c7d6e995e155c5551e729b74；正式Release latest，五平台checksum、Linux版本/commit、双架构OCI与稳定别名均实际核验。发布来源源码与四角色、失败及重试命令全部追加原证据账本。
- 验证：三态同输入输出与源码恢复哈希一致；补丁精确重建组合源码。前端165项、后端回归、PostgreSQL专项与分组缓存集成、远端unit/integration/lint/security通过。
- 边界：未部署生产，Stripe真实测试账户仍需启用前验收；源码回滚不能替代数据库备份恢复，也不能丢弃历史托管订单。无待发布操作。

- 文档整理：删除整合时误复制到顶部的支付历史日志副本，操作日志区原记录完整保留。

### 2026-09-18 — `20260918-stripe-experience` — 模式单选与前端体验本地交接

- 按用户五项要求，在 `9c8abed87` 的独立副本完成用户统一 Stripe 名称、管理员模式区分、收款模式单选、服务商文案和支付按钮改进。模式保存在既有 `payment_enabled_types`，未引入数据库迁移；历史服务商不因模式切换被删除。
- 后端配置更新和创建订单都验证模式互斥，旧双模式配置优先托管；保护已完成订单引用的密钥和币种，并用准确原因替换未完成订单提示。管理员订单筛选、详情和统计同步保留模式名称，避免两个同名筛选项。
- 133 项前端回归、i18n、定向 lint、类型检查、生产构建及带 unit 标签的支付相关后端测试通过；隔离 PostgreSQL 实测幂等、并发、到账和退款。浏览器使用本地模拟 API，覆盖手机／桌面、深浅主题、唯一 Stripe 入口、托管跳转、单选与历史服务商编辑；最终运行记录和截图位于固定验证账本及 `experience/screenshots/`。
- 首轮并行检查和 1536 MiB 堆限制导致内存中断，改为顺序执行及 2800 MiB 堆后通过；首次浏览器阴影断言把透明零尺寸阴影当作可见阴影，修正断言后通过。失败原始记录保留，没有当作成功。
- 同输入 BASELINE 显示 stripe 托管，MODIFIED 显示 Stripe 并提供 aria-pressed=true，ROLLBACK 恢复原样；归档哈希、补丁重建和重新应用均已验证。四角色路径保持不变，交接文档纳入最终重新封存。原主工作区不变，当前不提交、不推送、不发版、不部署。

### 2026-09-18 — `20260918-release-0.1.2` — 发布与验收完成

- 用户授权提交、推送、合并与发版；沿用原实现副本，功能与版本提交 `25a983458` 经 PR #44 的 16 项检查后正常合并为 `95a9576d5`，未调整分支保护。
- 产品 `VERSION` 与 provenance 晋级 0.1.2，兼容 0.2.5。annotated `v0.1.2` 指向功能合并提交，Release 工作流 35326891540 成功，正式非预发布且为 latest。主线与标签 CI、安全检查全部通过。
- 五个平台包实际下载并通过 SHA-256 复算，Linux amd64 二进制版本、兼容版本和完整 commit 正确；GHCR 双架构 OCI 与 0.1.2/latest/0.1/0 别名一致。详细结果在固定交付目录 `experience/release-0.1.2/RELEASE_RESULT.json`。
- 固定四角色继续使用 `MODIFIED_FILE.tar.gz`、`DIFF_FILE.patch`、`VERIFICATION.txt`、`ROLLBACK.sh`，本次更新包含版本与发布交接。BASELINE 为 stripe 托管／pressed=null，MODIFIED 为 Stripe／pressed=true，ROLLBACK 恢复基线；退出 0、恢复哈希相同、补丁重建和重新应用通过。
- 初次本地兼容审计因未跟踪的 node_modules 符号链接拒绝，临时移除该链接后审计通过并恢复链接；未将失败作为成功。业务源码复用前轮 133 项前端、支付后端与隔离 PostgreSQL、构建及四种浏览器场景验证。
- 本条仅更新交接文档，由受保护 PR 固化；发布标签不移动。无生产部署、数据库迁移或真实账号探测。最终文档合并事件追加原验证账本，后续动态状态以 GitHub 及机器结果为准。

### 2026-09-18T11:47:24.654076+00:00 — `20260918-sub2api-v0.2.6` — 同步及 RC 验收完成

- 预检 PR #46 和同步 PR #47 均通过全部保护检查并常规合并；使用固定 annotated tag 和标准 prepare，10 项冲突逐项裁决，完整三方报告保留。
- RC 五平台 checksum、双架构 OCI、旧版稳定别名不变、隔离新装/升级/回滚及持久化标记通过。无新增迁移，297 条历史 checksum 保持。
- 当前正在 release/0.1.3 将产品版本从 RC 晋级；正式发布结果由下一条收尾记录确认。完整命令、失败与成功、三态及四角色路径记录于独立证据目录。无生产操作。

### 2026-09-18T12:14:17.316465+00:00 — `20260918-sub2api-v0.2.6` — 正式发布与验收完成

- 预检 #46、同步 #47、版本 #48 通过全部保护检查后常规 merge；正式提交 `2ba6600027887abc119c2c7e9d5201b79228fe7c`，annotated v0.1.3 `bf46509902b6e42a7f674e353220e7f97c4cba38`，Release run `35342300372` 成功。未修改保护规则、移动标签或部署生产。
- 正式 Release latest=true，五平台包下载 SHA-256、Linux 产品0.1.3/兼容0.2.6/完整commit、amd64/arm64 OCI及稳定别名全部通过。镜像 digest `sha256:366408edf90f1cb08ecd2dfb7089c9484f4f8963ec7acd683c78681926fd1d58`。正式全新安装健康/登录200，297条迁移。
- RC 已覆盖 XY2API0.1.2升级及旧镜像回滚、官方Sub2API0.2.5数据库升级，持久化标记均保持。本地2303项前端、lint/types/build、后端定向与真实PG/Redis集成、票据生命周期race、Wire零差异和服务构建通过。
- 首轮5处导入排序、工具版本/包装器目录及新装端口冲突均已修复并保留失败记录。RC稳定别名保持旧版，正式版才更新。无新增SQL，297条历史checksum完全保持。
- 固定四角色重新封存最终源码，BASELINE/ROLLBACK为0.1.2/0.2.5，MODIFIED为0.1.3/0.2.6；exit0、恢复hash和补丁重建一致。清理结果及最终路径哈希见 `/xy/artifacts/upstream-sync-v0.2.6-xy2api-0.1.3/FINAL_RESULT.json`。原主工作区未改写，无生产或真实账号操作。


### 2026-09-19：292 / STATE 社区扩展本地融合

- 任务：`20260919-codex-state-upgrade`；用户批准按 `6c12e3f` 融合社区 `ecf3b9a`，在独立副本实现并验证，不推送、部署或调用真实账号。
- 实现：账号独立 Pro/Team 与多模型管理、双阶段完整响应复验、数据库条件租约和共享冷却、精确票据守护、WebSocket 逐轮桥接、IQ 等待／恢复复检／有限自愈、脱敏 API 和原账号弹窗集成。新增账号显式关闭，服务启动前完成旧全局范围幂等迁移，迁移失败不开放业务路由。
- 保留：IQ 判分与题目配置、分组提示词、模型映射和 compact、插件传输、计费及支付；不修改历史 SQL 迁移、模块依赖或前端锁文件。来源、适配说明见 `docs/third-party/` 和 `docs/CODEX_STATE.md`。
- 已实测：7 个受影响后端包完整 unit、STATE/IQ 定向回归、真实 PostgreSQL/Redis 集成、全部 7 个 Go 静态检查、service race；冻结原锁文件的前端 2319 项测试、lint、i18n、类型、生产构建和嵌入前端后端构建。1280/390 深浅主题表单、键盘、轮询及冷却通过；upstream sync audit 通过。
- 交付：`/xy/artifacts/codex-state-upgrade/work`，四角色为同目录的 `MODIFIED_FILE.tar.gz`、`DIFF_FILE.patch`、`VERIFICATION.txt`、`ROLLBACK.sh`。源码哈希及同输入 BASELINE/MODIFIED/ROLLBACK、仓储 race、补丁重建和回滚复原结果以 `FINAL_RESULT.json` 和字面命令日志为准；早期失败与资源中断没有删除或计为成功。
- 上线前独立步骤：测试账号至少两个续期周期与 24 小时观察。源码回滚不回退数据库；先停止新工作者、关闭 STATE 总开关，按维护文档处理程序和配置回退。

### 2026-09-19T17:04:14.826570+00:00 — `20260919-state-second-pass` — 二期本地实现、提交与验收

- 先提交一期 66 项改动，再提交二期后端一致性／扫描与调度优化及简化界面。核心提交分别为 `2a743f0`、`322b61486`、`49f0bd098`；没有远端和生产操作。
- 修复四个已复现边界，增加未来时间存量恢复、Retry-After 大数处理、撤销后同票期限保留和代理事务隔离；不改历史迁移、模块依赖或前端锁文件。
- 最终后端 unit、PostgreSQL／Redis、两组 race、全部启用 Go lint、前端 2321 项全量及定向回归、lint／类型／构建、嵌入构建和四种浏览器布局通过。旧失败、被替代的验证轮次和工具纠错均保留原始日志。
- 沿用原四角色，新增仅二期的基线与补丁作为补充。实际源码三态行为、退出码、字节恢复、源码哈希及四角色重开事件统一由最终 `VERIFICATION.txt`、`FINAL_RESULT.json` 记录；真实账号续期和 24 小时观察单独进行。

### 2026-09-19T17:54:54.836886+00:00 — `20260919-state-release-0.1.4` — 发布验收完成

- 已推送 STATE 功能分支，经 PR #50 全部门禁正常合并并发布 annotated v0.1.4；正式五平台包、版本、GHCR 双架构/别名及隔离新装已核验。
- 发布版本仅晋级 VERSION 与 provenance 产品版本，兼容 0.2.6、业务源码及历史迁移保持。原 /xy/xy2api 未改写，无生产部署或真实账号调用。
- 四角色及同输入三态、失败纠错、源码恢复哈希继续追加原验证账本；本收尾仅补交接文档，标签不移动。真实账号观察尚未执行。

### 2026-09-20T18:06:35.930885+00:00 — `20260920-state-reliability` — 本地实现与源码交付

- 基线为 `1a4fd39c71eadc4d5d587a39373bcded692e4fa5` / 0.1.4；源码位于 `/xy/artifacts/codex-state-upgrade/reliability-work`，分支 `feat/state-reliability`。用户要求验证后提交源码，并由用户后续推送；本轮不推送、合并、发布、部署或调用真实账号。
- 已实现 STATE 开关独立即时保存与修订冲突保护、持久手动获取任务、多代理加密配置和同出口检测、健康轮换、完整响应验证、候补与版本隔离、共享预算及独立质量观察。前端按钮和提示精简，预算/质量/诊断放入详情；管理员关闭不受打票中状态阻碍。
- 所有新策略默认关闭，逐步启用；质量隔离默认关闭。生产成功率、覆盖率、质量保持和 265 限流目标尚未实测，须执行批准的真实账号观察窗口；本地通过不代表这些目标已达到。
- 后端全量 unit、修复后 STATE 回归、service/parser race（-parallel=1 避免旧 Gin 测试全局变量竞争，保留用例内部并发）、真实 PostgreSQL/Redis STATE 集成与 repository race、全部 7 项 Go lint、Wire 生成、前端类型/i18n/生产构建、嵌入前端后端构建均已通过。前端全量初跑 2326/2327 通过，旧单代理 UI 断言更新后 64 项受影响回归通过，最后 113 项控制/设置/中英文回归通过。1280/390 深浅主题模拟 API 浏览器验证通过。macOS 专用脚本无法用 Linux 的 stat 参数完成，不计为通过。
- 一个并行只读子智能体完成多轮并发、代理、流验证与回滚脚本审查。静态检查发现的错误处理和表达式问题全部修复；race 发现的完成信号早于回调问题已修复，并原子化有界收尾测试计数；资源中断、一次运行中误清缓存造成的导出文件缺失与后续成功复验均保留字面记录，没有抹掉失败。
- 四角色继续使用 `/xy/artifacts/codex-state-upgrade/{MODIFIED_FILE.tar.gz,DIFF_FILE.patch,VERIFICATION.txt,ROLLBACK.sh}`。本次同输入三态、源码哈希、补丁重建、Git 提交和主仓库本地分支导入的最终事实以 `reliability-20260920/FINAL_RESULT.json`、`COMMIT_RESULT.json`、`LOCAL_BRANCH.json` 为准；这些记录在事务执行后生成。旧四角色在 `reliability-20260920/previous/` 留存。
- 原 `/xy/xy2api` 的 main 工作文件保持不变，完成的分支将以本地 Git fetch 导入供后续推送；提交树必须与已验证源码归档一致。源码回滚仅适用于独立副本，不操作生产数据库，不恢复过期或撤销票据。

### 2026-09-20 — `20260920-state-reliability` — 源码提交与三态验收完成

- 功能提交 `68d28d7aaa528977f167d815b46fd6381c6d05b0` 已创建，64 个文件；验证源码树与提交树一致。`/xy/xy2api` 已导入同名本地分支 `feat/state-reliability`，原 main / `9c8abed87987b5f4a8f278cdd411fe3ee7273713` 及工作文件保持不变。
- 同输入的手动软等待跳过、无效草稿下关闭行为为 BASELINE false/false、MODIFIED true/true、ROLLBACK false/false；三者 exit 0。回滚恢复 4,131 个文件与原树哈希一致；严格补丁重建 4,151 个文件与交付树一致，上游审计通过。首次严格补丁因本条新增交接文档末尾空行失败，格式已修复并重建通过。
- 本条交接只更新文档；最终分支头、四角色哈希及干净状态见交付目录的最终记录。未推送远端，未部署或执行真实账号成功率灰度。

### 2026-09-21 — `20260921-state-release-0.1.5` — 发布门禁与密钥修复

- 推送已验收的 STATE 功能及本地交接提交，创建 PR #52；保护规则要求八种检查且 strict=true，保持现有保护。产品 VERSION 和 provenance 的 xy2api_version 晋级 0.1.5，兼容版本 0.2.6 不变。
- 并行发布审查定位未配置固定 TOTP_ENCRYPTION_KEY 时代理密文无法跨重启解密的缺陷；发布前修复，具体测试及最终门禁结果由 release-0.1.5/ 执行日志记录。
- 隔离 0.1.4 基线已实测健康200、管理员登录200、297条迁移；仅本地合成数据，准备以最终制品验证升级、代理迁移/重启、旧版回切与再前滚。未连接或部署生产。

### 2026-09-21 — `20260921-state-release-0.1.5` — 正式发布与验收

- PR #52 最终16/16通过后普通合并，annotated v0.1.5 固定在 f0e60e852，Release/主线与标签 CI、安全检查全部成功。五平台 SHA-256、Linux版本、GHCR双架构与稳定别名均实际核验。
- 独立审查补齐固定共享加密密钥守卫，定向回归通过。新测试staticcheck失败已修正，gh旧版edit遇Projects弃用改REST成功；原失败保留。旧单代理迁移、两条密文、凭据脱敏、409、重启解密均在开发机本地验证。
- 开发机0.1.4→0.1.5升级及0.1.4旧镜像回切健康/登录/三类数据标记通过；297→298迁移仅追加。首个代理API验收因合成管理员缺少现有初始化确认返回423，补齐测试账号初始化后通过，未更改产品权限逻辑。
- 用户再次明确仅发布仓库、不动主站，并确认本机是开发机；本轮仅工作区与GitHub/GHCR操作，没有连接或修改主站。后续真实账号灰度仍须独立安排。再前滚后代理池解密与三类标记保留，正式版空卷新装健康/登录200、298条迁移通过。最终收尾PR与开发机资源清理结果由固定验证账本及FINAL_RESULT记录。

### 2026-09-22 — `20260922-admin-usage-metrics` — 本地实现与验收

- 完成管理员逐请求缓存命中率、生成阶段 Token 速度及默认开启的独立显示开关；权限沿用管理员设置路由，共用表格默认隐藏，公共接口不增加新字段。
- 137 项相关前端测试、类型、lint、后端定向回归和源码三态通过。最终生产构建、四种浏览器场景、封存哈希与四角色重开由 `/xy/artifacts/admin-usage-metrics/VERIFICATION.txt` 和 `ARTIFACTS.json` 记录；早期资源失败与修正过程保留。
- 独立分支 `feat/admin-usage-metrics`，主工作区无源码改动；无提交、远端或生产操作。临时编译 swap 清理完成。

### 2026-09-22 — `20260922-admin-usage-metrics` — 显示精简与算法复核

- 按用户补充要求移除表格百分比标签和速度波浪号，显示 `75.0%`、`50.0 T/s`；设置开关名称保留可识别文案。
- 核对缓存互斥分桶、毫秒换算及请求独立性，未发现既定公式算术错误；确认强制缓存计费会改写日志缓存数，输出含思考 Token、首字模式影响速度。已在中英文提示注明日志口径和估算限制，不改计费或采集协议。
- 137 项前端回归、lint、i18n、类型、生产构建通过；7 项后端定向测试含其子用例通过。1280/390 深浅主题浏览器模拟 API 验证均通过，并观察到纯百分比与无波浪号速度。源码审计脚本首轮匹配冒号过严失败，修正字段匹配后通过，原失败保留。
- 沿用原四角色，重新执行同输入 BASELINE/MODIFIED/ROLLBACK、补丁重建及恢复哈希比对；执行记录和最终哈希由固定 VERIFICATION.txt 与 ARTIFACTS.json 保存。原 main 干净，无提交、推送、部署或真实上游调用。
### 2026-09-22 — `20260922-usage-export` — 后台导出实现与隔离验收

- 已批准规范落地为独立usageexport包、任务表、API与两端共享任务界面；原列表语义及Heavy限流保留。默认关闭，允许名单灰度，前端无旧分页回退。
- 真实PostgreSQL快照/游标/领取锁、MinIO流式文件、HTTP授权、百万行边界、前端类型/构建/测试及浏览器原生下载均有字面记录。失去锁连接不得发布成功；同快照上传失败重试不重读数据。
- 曾发现XLSX整包压缩内存超标、迁移manifest层级错误及部分错误返回未检查，均修复并重测；全范围静态检查曾超时及编译进程被系统终止，最终串行批次通过（0 issues），前后端生产构建通过；失败批次仍保留。测试启动路径、浏览器缓存冲突和环境资源失败保留，不计为成功。
- 源码在独立worktree，未推送、部署或更改生产。四角色重建及回滚验证使用同6100条合成输入，原仓库文件哈希一致。上线前须部署等效完整HTTP P95/磁盘配额验收；源码回滚脚本仅恢复指定归档副本，不删任务表。

### 2026-09-22 — `20260922-usage-export-pr` — 提交与 PR 交接

- 用户授权将已验收实现推送并创建 PR，明确不合并、不发版。沿用 feat/usage-export 与四角色交付；业务代码不变，提交包含实现、测试和维护文档。
- 本地验证结果沿用上一条，PR #58（https://github.com/liulixin-lex/xy2api/pull/58）与提交身份记录在 /xy/artifacts/usage-export/PR_RESULT.json；远端 CI 状态以 GitHub 为准。

### 2026-09-21 — `20260921-gpt-quality-routing` — 会话降档防护本地实现与验证

- 实施前执行 `git pull --ff-only origin main`，固定 `021c0d885382ecfb956674e2f8b59a7674e8d912`。业务变更仅在 `/xy/artifacts/gpt-quality-routing/work` 的 `feat/gpt-quality-routing`；原工作区保持干净，未部署、未修改生产或调用真实模型。
- 增加 `gateway.openai_quality_routing`（off/observe/enforce，默认 enforce，默认避让 300 秒），严格比较最终出站模型和改写前的原始声明，仅已知 GPT-5.6+ 明确降档对触发。API Key、分组、会话、模型隔离；正常缓存身份不变，异常先换上游、再换凭据，必要时稳定轮换标识。
- 独立 Redis 状态与粘性键批量读取，Lua 代次和绑定 CAS 防止旧响应覆盖；本机先保护，远端更新预算 50ms。保留共享旧键，HTTP 成功不清除质量状态，不污染全账号质量统计。WS 在未发送下一轮时迁移，历史不足、压缩上下文和不完整工具续接继续绑定所有者。
- 回归补齐别名/日期/未知后缀、冲突及损坏声明、默认/高级/加权/父会话选路、租户隔离、稳定轮换、状态过期的新身份、Redis 超时取消、WS 完整历史重建与原生透传边界。service/repository race 和七项静态规则通过；首次测试连接关闭未检查返回值的问题已修正。
- 真实隔离 Redis 双客户端竞争和 TTL 通过。最后五轮健康请求 p95 增量最高 296 微秒，全部低于 2ms；3030 请求/3030 上游调用，正常正文与缓存标识一致。异常缓存模拟为 0/800/0/800，仅一次轮换冷启动；该结果不能外推为生产缓存率或真实首字保证。
- 同一探针输入在基线持续命中账号 1，修改版后续稳定迁到账号 2。四角色、补丁重建、实际回滚和哈希结果见 `/xy/artifacts/gpt-quality-routing/{MODIFIED_FILE.tar.gz,DIFF_FILE.patch,VERIFICATION.txt,ROLLBACK.sh,DELIVERY_RESULT.json}`；回滚依赖同目录校验过的 `BASELINE.tar.gz`，仅恢复独立源码副本。
- 验证中的磁盘不足、编译资源中断及 lint 分批重跑均保留原始失败记录。缓存和临时构建迁到本机 `/www/gpt-quality-routing-validation/` 后串行完成检查。功能说明为 `docs/OPENAI_QUALITY_ROUTING.md`；生产启用和真实效果观察未在本轮执行。

### 2026-09-22 — `20260922-quality-release-0.1.6` — 正式发布与验收

- 用户授权推送、合并和发版，并明确暂不处理 OAuth 指纹收敛；新增检测、身份轮换及 WS 连接代次收窄至 OpenAI API Key 凭据，新增 OAuth 排除回归，原指纹逻辑保持。
- 功能、版本和验证修复分开提交；PR #54 最终 16/16 通过后普通合并为 `f0127cc29`，没有关闭保护或强推。首次完整 CI 暴露两项旧调度指标空值断言，修复零值兼容与独立计数器断言后完整单元、集成、静态检查全部成功；最终质量/调度组合 race exit 0。
- annotated v0.1.6、正式 Release、五平台下载校验、Linux 二进制完整版本/提交、GHCR 双架构及稳定别名均实际核验；产品 0.1.6、兼容 0.2.6。Release run 35682383562 和主线/标签 CI、安全扫描全部成功。
- 最终候选五轮健康请求 p95 增量最高 1.224ms（此前本地实现轮次最高 0.296ms，历史记录保留），3030 请求/3030 调用且正常正文一致；异常缓存模拟只出现一次轮换冷启动。真实 Redis 的 32 个重复证据、双客户端代次及作用域隔离通过。
- 隔离 0.1.5→0.1.6→0.1.5→0.1.6 的健康、登录及三类数据标记通过，全新安装也通过，迁移数保持 298。专用测试资源完成清理，生产未连接、未部署、未修改。
- 原四角色继续位于 `/xy/artifacts/gpt-quality-routing/`，原版本已备份；发布源码同输入为 BASELINE first=1/next=1/stable=1、MODIFIED first=1/next=2/stable=2、ROLLBACK first=1/next=1/stable=1，全部 exit 0。补丁重建和恢复哈希相等已验证，跨磁盘移动失败及修复续跑、首轮 CI 失败等原始证据均保留。回滚脚本依赖同目录 BASELINE.tar.gz，仅恢复独立源码，不操作生产数据库。
- 发布标签不移动，收尾记忆通过独立受保护 PR 固化；最终源码/远端提交、交付哈希及清理结果见 `release-0.1.6/FINAL_RESULT.json`。真实生产缓存率和持续避让效果仍需后续部署观察。

### 2026-09-22 — `20260922-iq-session-isolation` — API 检测会话隔离与 0.1.7 发布

- 基线 `be451c280`，继续在 `/xy/artifacts/gpt-quality-routing/work` 的 `fix/iq-probe-session-isolation` 实施，原 `/xy/xy2api` 的 `021c0d885` 工作文件保持。IQ 独立探测此前不经过业务会话质量路由；现在每个 API Key 实际检测及持久化补试在最终模型映射、请求头覆写之后生成新会话与 `prompt_cache_key`，清除旧亲和、续接和大小写变体幂等头，保留指定账号、题目、判分、次数和禁止 POST 重放约束。
- OAuth IQ 不执行 API 随机会话轮换；新调用已有指纹收敛函数，off/device/session/full 按配置工作，设备+会话及完全收敛保持账号稳定 session/thread，逐轮 turn ID 仍遵循原逻辑。正常业务缓存与粘性未改变；IQ 检测放弃跨尝试缓存亲和，第三方是否换隐藏账号由上游实现决定。
- GPT-6 已在 0.1.6 覆盖，本轮补齐 `gpt-6` 别名、Astra effort/日期变体、正常模型与未知名称回归，文档明确 GPT-5.6、GPT-6 及更新 GPT 文本模型的已登记降档规则。0.1.6 Release 仅更正说明，标签、发布日期及六个附件 ID/大小/校验值回读一致。
- [PR #56](https://github.com/liulixin-lex/xy2api/pull/56) 最终 head `35b67f4402f2e2988d2640ebea36cbb705dabd8b` 在 16/16 检查成功后普通合并为 `934272ed828b05b4c558ae67837e46d67a86c520`；分支保护保持。[Release v0.1.7](https://github.com/liulixin-lex/xy2api/releases/tag/v0.1.7) 为正式 latest，annotated tag 固定 `934272ed828b05b4c558ae67837e46d67a86c520`；Release run `35696263868` 成功。五平台包下载校验、Linux 产品 0.1.7/兼容 0.2.6/完整 commit、GHCR 双架构与稳定别名通过，digest `sha256:f7d19c1ad818e50019058560b4d16ce8bc95178ef00716f7b8559075b863902c`。
- IQ、质量路由、指纹、STATE 定向回归与 race、七项静态规则通过；早期测试夹具误用头大小写/轮次字段、CI 静态写法、共享开发机内存不足导致的编译终止，以及工作树未提交时来源审计拒绝均保留原始失败记录，修正后门禁通过。没有真实模型请求。
- 同一合成号池输入中 BASELINE 三次共用一个隐式会话，结果 degraded/degraded/degraded；MODIFIED 三次使用三个新会话与缓存键，结果 degraded/smart/degraded；ROLLBACK 恢复基线。三态各三次上游调用，均 exit 0。这是可控模拟号池验证，不是生产抽样保证。累计业务质量探针仍为 first/next/stable：1/1/1 → 1/2/2 → 1/1/1。
- 开发机隔离 0.1.6→0.1.7→0.1.6→0.1.7 健康、管理员登录及 PostgreSQL/Redis/应用目录标记通过，全新安装通过，迁移数保持 298，专用测试容器已清理。未连接或操作生产。固定四角色仍在 `/xy/artifacts/gpt-quality-routing/`；累计补丁重建、源码回滚和恢复哈希由 `DELIVERY_RESULT.json` 与 `iq-session-isolation/FINAL_RESULT.json` 记录，源码回滚恢复原始 `021c0d885`，不操作数据库。

### 2026-09-22 — `20260922-admin-usage-metrics` — 推送及 PR 交付

- 用户授权提交、推送并创建 PR；功能提交 `017da5ac9` 已推送至 `feat/admin-usage-metrics`，PR #60 面向 main 开放评审。
- main 在开发期间新增质量路由和 IQ 会话隔离改动，合入 `f4cf08f5f`，唯一冲突为项目记忆文档，保留双方交接和历史日志；本功能业务源码与上一轮已验收归档一致。
- 四角色继续沿用 `/xy/artifacts/admin-usage-metrics/`，累计源码补丁以原 `021c0d885` 为基线，涵盖本次同步的 main；PR 差异仅为管理员使用记录功能及项目记忆。重新执行同输入三态和源码恢复验证，最终提交、远端 head、PR 可合并状态及 CI 观测由固定账本记录。不合并 PR、不发版、不部署。

### 2026-09-22 — `20260922-admin-usage-metrics` — PR #60 契约测试修复

- 用户要求检查并修复失败 PR。push/pull_request 两组 CI 均仅 test 失败，定位 `TestAPIContracts` 的管理员设置默认与 OAuth 配置回退两个用例：预期 303 个字段，实际 305 个字段。
- 在两处预期 JSON 中添加 `admin_usage_cache_hit_rate_enabled: true` 和 `admin_usage_token_speed_enabled: true`，继续严格匹配完整响应；产品实现和公共接口不变。前轮前端、静态及安全检查均已通过，原失败日志保留。
- 本地完整 API 契约与管理员开关回归、修复提交、远端最新 head 全部 CI、同输入三态、补丁重建/源码回滚及四角色重开事件追加原 VERIFICATION.txt，最终状态见 ARTIFACTS.json 和 PR_RESULT.json。不以推送成功代替 CI 成功，不合并 PR 或部署。
- PR 检查发现 Excelize GO-2026-5960，依照扫描给出的修复版本锁定2.11.0。主线冲突仅为交接文档，已保留双方记录；仅在功能分支接入主线，没有合并PR或发版。

### 2026-09-22 — `20260922-release-0.1.8` — 合并、发布与验收完成

- #58/#60 经保护检查正常合并；正式 v0.1.8 指向 `5b475476e`，Release/主线/标签工作流全部成功。版本与 provenance 分开提交，兼容基线保持0.2.6。
- 五平台包、Linux版本、双架构OCI和稳定别名一致；隔离升级、回切、再前滚及全新安装通过，299条迁移与持久标记保持，管理员设置权限实测通过。
- 固定四角色沿用原目录，包含同输入三态、源码恢复和补丁重建；初次合并测试导入遗漏、测试账号423及修复过程保留原记录。文档收尾不移动发布标签，不操作生产。

### 2026-09-23 — `20260923-sub2api-v0.2.8-sync` — 上游同步预检受阻

- 从干净 `main / 8a99582abaed61275b5f50b2ec55b98305e5c22e` 建立 `sync/sub2api-v0.2.8`。官方 GitHub Release 为正式、非 draft、非 prerelease；annotated tag object `d7a82d78ca51d42be41cb4daa3510ea401defe9f` 指向 `fd80b08c90b55edcad5b00171b53f08721d30da1`，未签名；共同祖先 `efe9aab1e4ec89a42ba45e8dac20e882c5409a6a`。仅配置 `upstream` fetch remote，push URL 为 `DISABLED`。
- 报告 `docs/upstream-sync/v0.2.8.json` 显示上游 508 文件、255 commits、双方 226 文件重叠；API 63、配置8、生成产物3 项受影响，新增 migration 238/239/240。当前 XY2API 已使用 238/239/240 编号实现不同 migration，需另行设计新编号并校验 checksums。
- 官方 `sync.py prepare` 按预期拒绝 43 项冲突中的 37 项清单外冲突，未创建 merge commit 或改动 main。冲突覆盖设置/DTO、计费、插件协议、网关核心、workflow 与忽略规则；不得绕过门禁。仅保留经审查的来源报告和任务启动提交 `4974f6612abe9ea23ba70d184869d612d8fcd170`。
- `doctor --strict` 初次指出未配置 upstream；配置后再次因当前位于同步分支、且环境缺少 Go 而未就绪。基础报告和 `git diff --check` 通过；尚未执行人工裁决、migration 重编号、生成代码、测试或 PR。下一步需按逐文件三方比较扩展 `manual_merge` 策略（仅有具体裁决时），解决 migration 冲突/重编号并追加 manifest，再运行所需 Go 工具链门禁。未合并、推送、发版或部署。

### 2026-09-23 — `20260923-sub2api-v0.2.8-sync` — 标准同步、发布与验收完成

- 在原预检基础上逐项登记 37 个清单外裁决路径，完成 43 项冲突裁决及真实上游 merge。保持 299 个已发布迁移字节，新增迁移顺延 253–255；保留 STATE/IQ/质量路由/导出/长上下文策略与兼容标识。
- 同步 PR #62 与晋级 PR #63 各在固定 head 的 18 项检查通过后正常合并；RC 与正式标签为 annotated，制品、镜像、版本与提交均已独立核验。最终发布指向 504f633ee5dfae6d21b541b276cb15da3dcbce3e。
- 前端完整 2629 项用例和生产构建、完整 Unit/Integration、安全及来源审计通过；RC 全新安装、0.1.8 升级与 PostgreSQL dump 恢复、正式全新安装均通过。运行数据验证覆盖迁移数、定价 JSON 和数据库/Redis/应用标记。
- 三态源码验收为 0.1.8/0.2.6/299 → 0.1.9/0.2.8/302 → 基线；补丁重建、回滚哈希和重新应用通过，四角色沿用 /xy/artifacts/upstream-sync-v0.2.8/。
- 早期测试失败、夹具 readiness 修正与工作流取消均保留证据。纠正执行过程中的判断：缺少实时日志不等于卡住；RC 首轮与正式 PR 前两次 CI 被过早取消，最终成功来自同一源码的完整重跑，未放宽任何门禁。
- 未部署生产、未调用真实模型账号；数据库回退使用升级前备份，不回写已发布 migration。正式标签保持不可变，项目记忆通过独立文档 PR 收尾。


### 2026-09-27：糖果检测当前健康与大响应兼容

- 原始副本 `/xy2/xy2api-original` 保持 `1cf9708e73c8c189968016852b07082e1c30e187`；修改在 `/xy2/xy2api`、`fix/iq-current-health`，未推送、未发版、未部署。
- 按用户决定只用糖果题21；当前状态每次尝试更新，错误即未知并避让业务流量，历史有效状态单独保留。正常1次，可恢复异常最多共3次，答错不补试；余额不足至少15分钟自动复检。
- 对指定生产请求的只读查询确认：旧解析器累计读取262145字节触及256 KiB限制。响应是HTTP200/SSE，不能据此断言实际答案错误或正确；原始回答未保存。新解析器 v5 总读取8 MiB、单事件2 MiB、答案64 KiB分开限制，保留完整终态和冲突校验；增加错误分类和限制诊断。
- Go IQ/质量路由单元、隔离PostgreSQL/Redis集成、核心race、迁移校验及服务构建通过；前端27项回归、类型、定向ESLint及生产构建通过。首次集成旧断言失败已修复，失败证据保留。补充socket超时测试结果见账本最新事件。
- 同输入基线错误后仍smart且可调度、长流21/29均unknown；修改后错误为unknown并阻断，长流分别smart/degraded。完整三态、补丁重建、回滚哈希以固定账本 `TRANSACTION_VERIFIED` 事件为准。
- 四角色固定为 `/xy2/artifacts/iq-candy-20260927/{MODIFIED_FILE.tar,DIFF_FILE.patch,VERIFICATION.txt,ROLLBACK.sh}`，源码回滚依赖兄弟 `BASELINE.tar`；不操作生产或数据库。操作说明 `docs/IQ_CURRENT_HEALTH.md`、`docs/OPENAI_IQ_CHECK.md`。后续部署需统一旧工作者状态语义并备份数据库；本轮未执行生产上线。
