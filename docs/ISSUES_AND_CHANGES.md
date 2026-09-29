# v0.2.1 问题与修复变更台账（修复 PR 版）

日期：2026-09-29 UTC。仅记录本次纯官方可靠性修复，不提交此前功能升级的源码或开发方案。

基线 e176641440258810df091ecd41e347e4d834fa24；修复提交 94ce2793177eadc595c2c6f6ef6207d222e4510f；原本地交接提交 18e5df70f。后续补充文档不改变已验证的业务代码。

导航：[问题检查报告](V021_ISSUE_SUMMARY.md) · [修复开发文档](V021_REMEDIATION_PLAN.md) · [完整修复与验证记录](V021_RELIABILITY_FIX.md)。

## 问题到修改的对应关系

| 问题 | 根因与实际修改 | 正式测试 | 本地结果 |
| --- | --- | --- | --- |
| XY-001 | openai_plugin_transport.go 拆分调度入口和不重入的实际发送 | openai_transport_dispatch_regression_test.go | 三故障、两对照通过，发送恰好一次 |
| XY-002 | plugin_manager_routing_test.go 注入调度器，保留OAuth失败关闭 | 既有插件回归及上述有界子进程 | 通过 |
| XY-007 | controlled_nonstream_response.go 添加统一读取错误分类；gateway_service.go保留cause；12个适配器文件承载13条同步路径接入 | gateway_reliability_regression_test.go、gateway_reliability_guards_test.go、handler/gateway_buffered_reliability_test.go、openai_oauth_passthrough_test.go | 真实截断/完整无效正文、认证handler、owner/取消/预算负向通过 |
| XY-008 | controlled_failure_domains.go 与 scheduling/failure.go接入可信协议失败；流式和缓冲观察路径使用既有PG冷却与身份fence | 真实隔离PG/Redis回归、协议正负例、failure_test.go | 健康键丢失和另一服务实例仍受冷却保护 |
| XY-009 | controlled_scheduling_dispatch.go保存未发送确定性，统一intent/settle pending | 真实dial refused、取消/超时、已发送未知、部分输出、重复Finish及确认幂等 | 通过，未修历史数据 |

完整适配路径、失败分类白名单、安全边界和源文件范围见修复记录。XY-003～XY-006为此前未提交功能与新版的兼容待办，不是本PR新增的运行故障或已完成改动。

## 验证账本摘要

- 原始复现保留：递归3失败/2对照通过；读取/冷却/结算诊断三轮24失败/24对照通过。失败是缺陷证据，不是修复成功。
- 全后端首轮61包通过、service包失败；仅两个测试场景需纠正：缓冲SSE改走完整handler而不是内部envelope校验器；compact stateless读取中断按新安全回退契约调整并追加强owner负向。之后整个service包8022顶层/7950子项通过，11既有skip；不改写首轮exit1。
- 隔离repository/migrations701顶层/1206子项通过；handler三轮race54/60、scheduling race160/113、service关键三轮race21/237及广泛守卫104/555通过，无数据竞争。
- 两轮lint均0 issues/exit0；后端构建及版本运行、14项同步工具测试、干净提交来源审计通过。测试次数是调用次数，重复三轮不等于三倍独立用例。
- 首轮41个unit skip、集成4个skip及worker入口等边界在修复记录中说明；调度receipts/health/inspection三项真实Redis测试后来已补跑。没有把真实供应商/S3/外部审计环境等跳过项写为通过。

## 纠正与未覆盖范围

- 新测试JSON转义错误、Images夹具方法名错误、race二进制错误工作目录及系统缺python命令均保留失败，分别修正后重跑，不归为产品缺陷。
- Codex direct images原本已有外层读取错误转换；初次要求内部直接返回typed error的错误断言层级已纠正，不凑成新增问题。
- 本轮handler经过真实认证中间件及Gin路由，但数据仓库为合成stub、简单模式隔离费用，不声称数据库认证或真实付费端到端。
- 不改前端、迁移/校验和、版本/provenance、默认调度阈值、站点配置及历史数据。未执行旧功能组合、真实供应商、浏览器、跨平台发布或部署验收。
- 三个专用临时PG/Redis容器及tmpfs合成数据已按完整ID/标签清理；源码、构建产物与成功/失败日志保留，既有站点未操作。

## 操作记录（追加）

### 2026-09-29：修复与本地提交

完成18个生产Go文件、7个测试文件及原修复记录/交接/白名单共28文件的提交94ce27931，追加交接18e5df70f。本地验证通过；当时GitHub登录缺失，未推送或创建PR。

### 2026-09-29：仅修复 PR 提交准备

用户确认服务器登录已配置，并明确只提交此次修复及问题/修复/验证文档。确认账号对个人fork有推送权限、对官方仓库无直推权限，使用既有fork发起面向官方main的PR。官方main复核仍为固定基线；补齐本台账、问题报告和修复开发文档，原功能代码及其详细开发方案不提交。最终PR编号、远端head与CI状态见项目记忆和PR页面；不自动合并或部署。
