# v0.2.1 网关问题检查报告（修复 PR 版）

日期：2026-09-29 UTC。固定检查基线：e176641440258810df091ecd41e347e4d834fa24；相关业务代码与正式 v0.2.1 的 857495c876e3fa33df026d058d5600a099183776 相同。

本文件从本地检查记录中整理，只随本次纯官方可靠性修复提交。**不提交此前的多分组、动态优惠或界面升级代码，也不提交其功能开发方案。** 修复提交为 94ce2793177eadc595c2c6f6ef6207d222e4510f。诊断失败、修复结果与未验证边界分别记录，不能把本地通过当作已合并、发布或上线。

导航：[问题与变更台账](ISSUES_AND_CHANGES.md) · [修复开发文档](V021_REMEDIATION_PLAN.md) · [修复与验证记录](V021_RELIABILITY_FIX.md)。

## 问题总表

| ID | 性质 | 已确认问题 | 本 PR 状态 |
| --- | --- | --- | --- |
| XY-001 | P0 | OpenAI 调度旁路无限递归，导致进程栈溢出 | 已修复，本地验证通过 |
| XY-002 | 测试覆盖缺口 | 相关插件测试未注入生产形态调度器，漏掉递归入口 | 已补正式回归和依赖注入 |
| XY-007 | P1 | 非流式读取中断没有进入安全回退，部分适配器提前写 502 | 13 条同步路径已修复，本地验证通过 |
| XY-008 | P1 | 标准服务端协议失败没有形成 PostgreSQL 权威冷却 | 已修复，真实隔离 PG/Redis 验证通过 |
| XY-009 | P2 | 明确未发送的尝试仍挂 usage_pending | 已修复，发送确定性及幂等验证通过 |

检查记录中的 XY-003～XY-006 分别是未提交功能与新版调度、迁移历史、响应/计费和管理 API 的兼容事项，不是四个已复现的官方运行缺陷；本 PR 不实现或关闭这些事项。旧功能源码及详细方案继续保留在本地，不在本提交范围。

## XY-001 / XY-002：入口递归与测试覆盖

- 位置：backend/internal/service/openai_plugin_transport.go 的 doOpenAIUpstream，与 controlled_scheduling_dispatch.go 的 roundTrip 旁路分支。
- 根因：调度入口把自身当成终端发送回调；GET、缺少请求调度上下文或 Sub2API 原版模式返回 nil dispatch 后，回调重新进入同一入口，没有到达真实传输。
- 复现：子进程限制栈为 128 KiB，父进程期限 20 秒；上述三个场景均 fatal stack overflow、底层 stub 零发送。无调度器和已有 dispatch 标记两个对照只发送一次，未使用真实网络。
- 版本边界：自回调已存在于 v0.2.0，v0.2.1 增加原版模式旁路；旧版本结论来自源码比较，运行复现固定在本次 v0.2.1。不是此前功能升级引入，也不能称 v0.2.1 独有。
- 测试缺口：原四项相关插件测试通过，但 gateway 夹具未注入 controlledScheduling。不能由此扩大为全仓没有测试。
- 修复：拆出 sendOpenAIUpstream 终端发送函数；保留插件、代理、OAuth fail-closed 和观察生命周期。正式测试补注入并覆盖三故障、两对照。

## XY-007：响应完整读取前的中断不能安全回退

- 原复现六条：raw Chat、Responses、Anthropic Messages、OpenAI Images、Gemini→Messages 和原生 Gemini。真实本机 HTTP 声明过长 Content-Length 后截断，产生 unexpected EOF。
- 旧行为：均返回普通错误而非 UpstreamFailoverError；raw Chat 和 Gemini→Messages 还提前写 502。六个完整读取无效 HTML 的对照则返回 typed failover 且没有写客户端响应。
- 原因：读取错误早于完整性校验，绕过既有失败分类；handler 依赖 typed error，且响应一旦提交即不能回退。
- 相邻复查增加七条同类路径：Responses passthrough、Anthropic passthrough、Bedrock、原生 Anthropic 兼容、Chat 转换管线、Gemini→Chat、Embeddings。
- 修复：统一可恢复的 2xx 读取错误分类，在写错误前交给原 handler；保留底层 cause，不新建重试循环、不重置预算或 deadline。
- 边界：取消、总超时、不可重放、强 owner、语义已提交、预算耗尽、响应上限与未知错误不扩大重试。Codex direct images 已有外层转换，最初要求其内部直接返回 typed error 的测试层级错误保留记录，不算新增缺陷。

## XY-008：协议失败缺少权威冷却

- 位置：controlled_scheduling_dispatch.go 的 ObserveFrame/Finish、controlled_failure_domains.go、scheduling/failure.go。
- 旧行为：HTTP 200 内 response.failed/server_error 仅更新 Redis 健康，PG 失败证据未接入，冷却记录为 0、Eligible=true；HTTP 503 对照写入一条冷却、Eligible=false。
- 真实隔离 PG/Redis 对照：删除该账号模型一个 Redis 健康键后，旧 SSE 失败账号在 30 秒内重新入选，503 对照仍被阻止。未删除运行站点的任何状态。
- 修复：完整解析、验证的服务端失败事件进入原失败证据/终态意图/身份 fence/PG 冷却；范围仅账号与模型，不能依 SSE 声称扩大为共享池故障。
- 边界：不把用户参数错误、取消或未知字符串当服务端故障；保留原 30 秒普通冷却及特殊错误分类。修复后流式、缓冲 SSE 和 HTTP 503 均有真实存储回归，并由另一服务实例确认仍不能提前重选。

## XY-009：明确未发送仍标记待结算

- 位置：controlled_scheduling_dispatch.go 的 MarkSent、roundTrip、Finish 及既有结算调用。
- 复现：本机关闭端口的真实 dial/connection refused，旧 PG 记录为 settled/not_sent/proven_not_sent，但 usage_pending=true；HTTP 400 对照为 false，两场景各三轮一致。
- 根因：MarkSent 表示进入传输尝试，不保证已经发送字节；旧 pending 判定 sent && status<400 在拨号失败时被 status=0 误触发。
- 修复：在取消/超时改写 outcome 前保存 proven_not_sent，terminal intent 与 settlement 使用同一 pending 判定；保留尝试审计及合理失败避让。
- 边界：已发送结果未知、部分语义输出、迟到用量和待入账状态仍保守处理。**这是待结算标记不准确，不是已证实重复扣费或资金损失。** 本 PR 不修改历史数据库记录。

## 证据与验收范围

修复前递归诊断为 3 FAIL / 2 对照 PASS；后续六条读取、协议冷却和未发送结算诊断三轮合计 24 FAIL / 24 对照 PASS。它们是缺陷证据，不是修复通过。

修复后的完整结果、首次失败及纠正、跳过项和测试命令见 [修复与验证记录](V021_RELIABILITY_FIX.md)。正式测试已随代码提交，可用新建隔离存储复跑；原始运行日志保留在开发机，不上传环境文件、密钥或生产数据。真实供应商、线上部署、历史数据修复与旧功能组合验收不在本 PR 范围。

## 2026-09-30：原 PR head 的新增反证

固定85d51657后，独立审查确认原版递归和未发送pending修复有效，同时发现并补修三处缺口：

- XY-007A：内部attempt正文超时被当作客户端取消；原三适配器反例typed_failover=false，修后13条同步路径实际先取消首上游，再在同ledger上接受账号2响应。客户端/管理员取消仍不得借此回退。
- XY-008A：普通JSON error和root response failed漏PG冷却；原版删除Redis键后另一实例重选，修后PG gate阻止重选，取消/未完成不误判。
- XY-010：simple模式下显式组混入其他组候选及跨组owner；修后普通选择/强owner同时严格限定显式组，默认组和Sub2API路径保持原语义。

这三项属于现有可靠性和既有显式分组规则修复，不是PR #73功能组合。完整新反例、补丁身份、运行计数和未覆盖边界见修复记录末节。最终组合制品、生产升级及远端CI仍须按对应head验收，健康页200不能替代业务路径验证，也不承诺零生产风险。
