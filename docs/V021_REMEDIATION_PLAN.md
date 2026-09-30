# v0.2.1 可靠性修复开发文档（修复 PR 版）

日期：2026-09-29 UTC。基线：e176641440258810df091ecd41e347e4d834fa24。修复提交：94ce2793177eadc595c2c6f6ef6207d222e4510f。

本文件整理已执行的纯官方 D0～D4/G1 修复设计与验收要求；不纳入此前功能升级的 D5～D8 开发方案。用户仅授权将修复、测试和问题/验证文档提交 PR，由维护者决定是否合并；不合并、发布或部署。

导航：[问题检查报告](V021_ISSUE_SUMMARY.md) · [问题与变更台账](ISSUES_AND_CHANGES.md) · [实际验证结果](V021_RELIABILITY_FIX.md)。

## 任务与实现

| 任务 | 问题 | 实现及完成条件 | 当前结果 |
| --- | --- | --- | --- |
| D0 固定基线 | 全部本次缺陷 | 独立副本、本机 HTTP、临时 PG/Redis；保存相同输入的失败与对照 | 完成，原始失败保留 |
| D1 终端发送边界 | XY-001/002 | doOpenAIUpstream 调度回调仅调用 sendOpenAIUpstream，不重入；补生产形态依赖注入与有界子进程 | 三故障及两对照通过 |
| D2 读取错误回退 | XY-007 | nonstreamReadError 共用分类器接入13条同步路径，写响应前交由原 handler；保留原始 cause | 截断/无效正文对照、真实认证 handler 与停止守卫通过 |
| D3 协议失败冷却 | XY-008 | 可信服务端协议失败进入现有 FailureEvidence、终态意图、PG 反馈和身份 fence | HTTP503、SSE、缓冲SSE、丢失健康键与第二实例通过 |
| D4 发送确定性 | XY-009 | 保存未发送证据并统一 terminal intent/settle 的 pending | 拨号、取消/超时、未知结果、部分输出、重复Finish及用量确认通过 |
| G1 官方修复门禁 | 上述四缺陷和覆盖缺口 | 后端unit、隔离集成、定向race、lint/build、来源与迁移检查 | 本地完成，远端CI按PR实际head另查 |

## 不可放宽的调度约束

1. 保留每请求固定模式与分组策略、账号优先级/权重、实际尝试账本、总次数和 deadline。失败换号不能重新创建预算。
2. 不调整默认单次首输出 120 秒、总等待 240 秒、最多 3 次，实际仍使用原有效配置；不重新引入旧模型阈值或 TPS 判定。
3. 保留普通账号池 30 秒冷却、特殊错误分类、暂停/权限/准入、身份与控制代次 fence；未知协议证据不能扩大失败作用域。
4. UpstreamFailoverError 只是候选失败分类，下一次实际派发仍由已有调度器批准；全局配额、owner、不可重放和耗尽预算不得被换号绕过。
5. 本 PR 不增加跨组路由，不改变费用计算，也不修改历史 SQL、版本、来源元数据或运行数据。

## 安全回退契约

- 完整读取和解析之前不提交非流式语义内容；可恢复连接读取中断交回既有 handler，禁止先写 JSON/SSE 错误再尝试回退。
- 已输出文字、工具调用或媒体语义后禁止重放；安全心跳与语义输出继续使用原提交守卫，不假造跨账号续接。
- 当前请求已取消、总 deadline 到期、客户端断开或原账本耗尽时停止。区分单次上游读超时与总请求超时。
- 请求体、模型、会话、选项和客户端语义不因回退改变；不额外套一层独立重试循环。
- 响应上限、4xx、未知错误不一律转换成可重试。WebSocket、异步任务、视频与强 owner 不扩大重放范围。

## 冷却与结算契约

- 仅完整、可信的服务端失败事件进入协议故障证据；服务端错误白名单与用户错误负向回归同时保留。
- 复用现有 PG 冷却、终态意图、失败反馈、幂等和恢复机制，不建第二套旁路状态；Redis 健康键丢失不能取消 PG 权威拒绝。
- outcome 被取消/超时重命名前保存已证明未发送的事实；usage_pending=false 不等同伪造用量已确认。
- 对已发送但结果未知、部分输出、待入账或迟到用量保持保守状态；重复 Finish、并发确认不得重复结算。
- 不提供历史 pending 的自动 UPDATE。历史筛查、备份及数据修复需要单独授权。

## 验收设计与实际证据

保留原检查编号，便于与本地问题记录核对。下表是设计要求；具体实际通过范围、测试夹具边界及跳过项以 [修复与验证记录](V021_RELIABILITY_FIX.md) 为准，不把场景清单本身当作所有端到端均已通过。

| 编号 | 本 PR 对应的纯官方要求 |
| --- | --- |
| T01～T04 | 调度旁路不递归；插件/OAuth/正常受控派发与取消保持一次发送及资源/结算约束 |
| T05 | 原六条及新增七条截断适配器安全 typed failover，完整无效正文对照保持 |
| T06 | 实际API Key中间件与Gin handler验证同请求接管；新增测试为Responses/raw Chat/Images四场景，仓库为合成stub，不冒充数据库认证端到端 |
| T07～T10 | 首语义前/后、心跳、取消/deadline、owner、预算、正文上限与读取完整性保护不退化；不包含旧功能的跨组组合验收 |
| T11～T13 | 标准协议失败和503产生权威冷却；健康键丢失、双实例、身份/控制代次、幂等及用户错误负向保持 |
| T14～T15 | 真实dial refused无pending，HTTP400对照保持；已发送未知/部分输出/迟到用量不误清 |
| T16 | 重复Finish和用量确认幂等；不声称完成活动或多组计费端到端 |

T17～T30 及 T06～T10/T16 中的旧功能跨组/活动部分均不随本 PR 实施、验收或提交功能方案。

## 复验和发布边界

从 backend 目录运行，使用 go.mod/CI 对应工具链和**新建、已核验身份的专用测试 PG/Redis**；相关夹具会清理测试 Redis DB15，禁止指向任何运行站点实例。缺少存储导致的 SKIP 不算实际存储验证。

    go test -p 1 -parallel 1 -tags unit ./internal/service -run 'Test(ReviewV021|NonstreamReadErrorSafety|ControlledProtocolFailureEvidence|ControlledTerminalUsageCertainty)' -count=3
    go test -race -p 1 -tags unit ./internal/handler -run '^TestGatewayBufferedReadFailureAuthenticatedFailover$' -count=3
    go test -p 1 -parallel 1 -tags unit ./... -count=1

此外按仓库现有门禁运行 repository/migrations 集成、调度与服务定向 race、lint、后端构建，以及干净提交上的 python3 tools/upstream-sync/sync.py audit。定向命令不能代替全量回归；本轮只补交文档时不把历史测试写成重新执行。

原始失败和通过日志分开保存；全量首轮两个测试契约/层级问题的纠正和 service 整包复验均需如实保留。前端无变化，不据此声称本次执行浏览器、真实供应商、跨平台制品或生产验收。发布、部署、历史数据修复和旧功能整合均不在提交 PR 的授权之内。

## 2026-09-30：独立审查补充执行项

在原PR74 head85d51657上完成三个最小补修：

| 项目 | 固定契约 | 新执行证据 |
| --- | --- | --- |
| D2a / XY-007A | 内部attempt超时与真实caller/admin取消分离；沿用原总次数/总等待/owner/输出提交限制 | 13适配器实际取消首上游后同ledger账号2成功；取消边界及race通过 |
| D3a / XY-008A | JSON与SSE同一服务端失败证据；根response状态可识别，但incomplete/cancelled仍排除；PG为权威 | 两原JSON反例均补写PG gate，Redis键丢失后第二实例仍拒绝重选 |
| D3b / XY-010 | 显式分组优先级/权重仅作用本组成员；owner同样必须授权；group0遵守运行模式 | simple跨组反例、owner拒绝、40次分组独立权重和1/0/1三态通过 |

保留初次失败及原有跳过。新定向和race结果只证明其实际覆盖；最终全量组合、构建制品、升级烟测与远端CI必须绑定交付HEAD。三补修不加入PR73前端/IQ功能、不改发布版本/迁移/历史pending、不执行生产操作。
