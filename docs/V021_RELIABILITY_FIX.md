# v0.2.1 网关可靠性修复记录

日期：2026-09-29（UTC）。基线：e176641440258810df091ecd41e347e4d834fa24（相关业务与正式 v0.2.1 相同）。

## 范围和交付状态

本次仅修复官方版本的四类可靠性缺陷及相关测试缺口。不包含动态优惠、多分组密钥、管理界面或新版功能整合，不修改版本、历史迁移、调度默认值或数据库数据。不部署、不调用真实付费上游。修复后由维护者审查 PR，不能把本地通过当作已合并、已发布或已上线。

当前：D0～D4/G1 本地完成，修复提交 94ce2793177eadc595c2c6f6ef6207d222e4510f。后端 unit、关键 race、隔离存储集成、最终 lint、构建及干净提交后的来源审计通过。尚未推送或创建 PR。服务器 HTTPS 推送预检因缺少登录身份退出 128；邮箱不是 GitHub 登录凭据，没有借用其他项目的密钥。PR 草稿保存在候选父目录 PR_BODY.md。

## 问题与实际修改

| 问题 | 根因 | 修改及边界 |
| --- | --- | --- |
| XY-001 / P0 | doOpenAIUpstream 把自身作为调度旁路发送回调，导致栈溢出 | 拆分 sendOpenAIUpstream 终端发送函数；保留插件、OAuth 失败关闭和请求选项。旁路、原版模式、已有 dispatch 均只发送一次 |
| XY-002 / 覆盖缺口 | 插件网关测试没注入生产形态调度器 | 补注入；有界子进程测试覆盖原来三个崩溃场景及两个对照 |
| XY-007 / P1 | 非流式读取中断直接返回普通错误，部分适配器先写 502，无法由 handler 接管 | nonstreamReadError 仅归类可恢复的 2xx 读取错误；保留原始 cause。取消、总超时、响应上限、未知错误、owner、不可重放、语义输出、耗尽预算不新增重试；最终派发仍须通过原调度准入 |
| XY-008 / P1 | HTTP 200 内标准服务端失败事件只有 Redis 健康避让，没有 PG 权威冷却 | 完整协议事件的受信服务错误接入既有 FailureEvidence、终态意图、PG 失败反馈和身份 fence；仅账号/模型作用域，不根据 SSE 声称扩大共享故障 |
| XY-009 / P2 | MarkSent 表示进入传输尝试，但拨号失败已证明未发送时仍设置 pending | 依据 proven_not_sent 统一终态意图和结算 pending；在取消/超时改写 outcome 前保存发送确定性。已发送结果未知、部分输出及等待用量确认路径不清除 |

XY-009 是待结算标记不准确，不是重复扣费证据。本次不修改历史 pending 记录，不提供自动 UPDATE。

### XY-007 路径清单

原六条：raw Chat、Responses、Anthropic Messages、OpenAI Images、Gemini→Messages、原生 Gemini。

沿相邻真实同步 HTTP 路径复查，又复现并接入七条：Responses passthrough、Anthropic passthrough、Bedrock、原生 Anthropic 兼容、Chat 转换管线、Gemini→Chat、Embeddings。每条采用相同分类器，不引入第二个重试循环。

Codex direct images 已有独立的读取错误包装及外层转换协议；初次扩展测试错误地要求它在内部适配器直接返回 UpstreamFailoverError，失败记录保留，但不作为新增缺陷或本次修改。其既有测试继续回归。视频、异步和 WebSocket 不扩展重放范围。

### 不改变的调度及回退规则

- 沿用请求固定模式、分组策略、总次数与总 deadline、账号优先级和权重。换号不能重新建立预算；原 30 秒账号池普通冷却及特殊错误分类保持。
- UpstreamFailoverError 只表示可考虑回退，不是无条件重放许可；实际选路继续验证暂停、权限、owner、共享准入与剩余预算。
- 未提交语义内容时可由原 handler 同请求重试；已经输出文字、工具或媒体内容后禁止重放。安全心跳与语义输出仍由原提交守卫区分。
- 同一尝试 Finish 幂等；失败尝试保留审计，不伪造用量确认，不增加用户扣费路径。

## 测试和证据

新增正式测试：

- openai_transport_dispatch_regression_test.go：五场景、128 KiB 子进程栈上限、20 秒父期限，无外部网络。
- gateway_reliability_regression_test.go：本机真实 HTTP Content-Length 截断、完整无效正文对照、PG/Redis 调度；SSE/503 冷却对照及第二服务实例丢失单个 Redis 健康键；真实 dial refused 对照。
- gateway_reliability_guards_test.go：读取错误/重放停止条件，流式与缓冲协议证据正负例，取消/超时与未发送确定性、重复 Finish 及并发用量确认。
- gateway_buffered_reliability_test.go：真实 API Key 认证中间件和 Gin 路由、真实本机 HTTP；Responses、raw Chat、Images 的成功接管、耗尽一次最终错误、取消零后续派发、未认证零发送。仓库数据为合成 stub，非数据库认证端到端；简单模式隔离费用，受控预算另由真实存储测试验证。

本地原始日志位置：/opt/xy2api-v021-fix-20260929-anp5W2/。只连接带本任务标签的临时 PG17/Redis8.4；测试会清理专用 Redis DB15，禁止连接运行站点。

| 日志 | 结果 / 意义 |
| --- | --- |
| d0-baseline.jsonl | 原六条读取、协议冷却、未发送 pending 的八个失败场景仍失败；D1 已先修复。不是全部纯基线首次运行 |
| d1-regression.log | D1 和插件回归通过 |
| d1-d4-regression.jsonl | 原四类回归及对照连续三轮通过 |
| guards-first.jsonl | 新测试 JSON 字面量转义错误导致编译失败，随后修正 |
| guards-second.jsonl | 负向分类、协议证据、确定性/并发确认和原四类回归通过 |
| adjacent-baseline.jsonl | 七个相邻读取中断复现；direct images 两个断言因层级不匹配排除，未掩盖为修复 |
| handler-first.jsonl | 新夹具引用不存在的 ImagesGenerations 方法，编译失败；改用现有 Images |
| handler-second.jsonl | 三入口四场景共十二项通过 |
| backend-unit.jsonl | 首轮 exit 1：61 包通过、service 包两个场景失败，见下方测试修正记录；41 个跳过保留 |
| service-unit-final.jsonl | 修正后整个 service 包重跑 exit 0，8022 顶层及 7950 子项通过、11 个既有跳过；结合首轮其余 61 包构成最终 unit 结果，不把首轮失败改成成功 |
| repository-integration.jsonl | exit 0；2 包、701 顶层及 1206 子项通过，4 项既有跳过；独立 Testcontainers PG/Redis，包含迁移 |
| handler-race.jsonl | exit 0；三轮 54 顶层及 60 子项通过，无跳过、无数据竞争 |
| scheduling-race.jsonl | 首轮独立测试二进制从 backend 而非包目录执行，迁移相对路径错误；保留失败，不归因于业务 |
| scheduling-race-final.jsonl | 正确包目录重跑 exit 0；160 顶层及 113 子项通过，1 个父测试专用 worker 入口跳过，无数据竞争 |
| service-race-regression.jsonl | exit 0；四类故障及新增守卫三轮共 21 顶层及 237 子项通过，无跳过、无数据竞争 |
| service-race-guards.jsonl | exit 0；广泛调度/准入/身份/用量/插件守卫 104 顶层及 555 子项通过，1 个 worker 入口跳过，无数据竞争 |
| backend-lint.log / backend-lint-final.log | 两轮均 exit 0、0 issues，后者在最后一次测试源码修改后执行 |
| backend-build.log | exit 0；后端构建及版本运行通过，非正式发布制品 |
| sync-tests-final.log | exit 0，14 项同步工具测试通过；此前系统无 python，改用 python3，原失败日志保留 |
| sync-audit-final.log / sync-audit-clean.log | 前者提交前按规则拒绝 dirty 工作树；后者在修复提交后 exit 0，upstream sync audit passed，没有绕过门禁 |
| VALIDATION.json | 外部日志统计与 SHA256 索引；次数是测试调用次数，三轮结果不冒称三倍独立用例 |

### 全量回归发现的测试修正

- buffered_sse 新测试最初要求内部 envelope 校验器直接报错，但该层合法 failure envelope 可返回 nil 且 healthy=false。已改为走完整非流式 response handler，验证 typed failover、零客户端输出及真实 PG 冷却，未放宽业务断言。
- 既有 compact read_error 断言要求 stateless unexpected EOF 不可回退，与本次安全读取修复的目标相反。更新为可回退，同时保留 errors.Is 原始错误与零输出断言；新增强 owner 的实际 Forward 负向，确认不能因此重放。
- 两项修正均仅涉及测试；原全量失败日志不覆盖。生产源码在后续完整 service 复验和 race 所用版本间保持相同。

### 跳过项与覆盖边界

首轮全后端 41 个跳过的具体名称和原因在外部 VALIDATION.json。包括真实供应商/TLS 网络、外部 S3、审计/导出专用环境、Stripe PostgreSQL 夹具、插件进程包、opt-in 容量门禁、历史 NULL 模式和父测试 worker 入口。不声称这些已运行。

其中调度专用 Redis 的 receipts、health、inspection 三项已配置独立端点，在 scheduling race 中实跑通过。集成仍跳过既有 GetAccountsLoadBatch TODO，以及导出 snapshot、S3 和 load-gate；service 与 scheduling 的 worker 跳过是供父进程派生调用的入口，父测试仍执行。

本次没有前端变化，因此不重复前端/浏览器门禁；没有真实供应商、真实付费计费、运行站点数据库、线上备份恢复或跨平台发布验收。没有借助这些跳过放宽本次修复涉及的本地 PG/Redis、handler、预算、owner 或发送确定性验证。

全部存储测试结束后，按完整容器 ID 与任务标签复核并移除本轮 PG、unit Redis、race Redis 三个临时容器及 tmpfs 合成数据，任务标签查询无残留。源码、原始成功/失败日志和构建产物保留；不删除或操作已有服务容器。构建二进制 SHA256 为 c2b7562c00a64c6935c384c1c77667f81b3e57da03de15d2c9154ab3569f3775，仅供本地验证。

### 修改文件范围

- 核心：openai_plugin_transport.go、controlled_nonstream_response.go、controlled_failure_domains.go、controlled_scheduling_dispatch.go、gateway_service.go、scheduling/failure.go。
- 接入：十二个网关源文件承载上述十三条同步非流式路径（Gemini 兼容文件包含两条）。代码差异只替换读取错误分类或在写错误前交给已有 failover。
- 测试：新增四个正式测试文件，更新 failure_test.go、plugin_manager_routing_test.go、openai_oauth_passthrough_test.go；不删除已有测试文件。
- 文档：本记录、项目记忆及本文件的精确 .gitignore 白名单。不改变 frontend、backend/migrations、UPSTREAM_BASE.json、同步 policy、VERSION 或 SUB2API_COMPAT_VERSION。

定向命令（在 backend 中执行；环境变量须指向独立测试实例）：

    go test -p 1 -parallel 1 -tags unit ./internal/service -run 'Test(ReviewV021|NonstreamReadErrorSafety|ControlledProtocolFailureEvidence|ControlledTerminalUsageCertainty)' -count=3
    go test -p 1 -tags unit ./internal/handler -run '^TestGatewayBufferedReadFailureAuthenticatedFailover$' -count=3
    go test -p 1 -parallel 1 -tags unit ./... -count=1

验收边界：本轮对应修复开发文档 D0～D4/G1；T01～T16 结合新旧测试检查，T06～T10/T16 的活动与跨组部分仍属于后续 D5。T17～T30、三来源功能迁移、浏览器、真实供应商和部署未执行。不得据纯官方修复宣称新版功能合并已完成。
