# 可控调度实施验收映射

日期：2026-09-27。本文对应隔离候选实现，不宣称已经上线或79项业务端到端验收全部完成。

原始矩阵：/xy2/artifacts/iq-candy-20260927/scheduling-research-20260927/ACCEPTANCE_MATRIX.json，79条当前用例。两条已废止TPS-only用例不计入。V3/V4/V5参考模型52/37/53条断言不是Go、PostgreSQL、Redis或协议验收证据。

## 状态与实现索引

核心通过只证明所列命令的断言；miniredis执行真实Lua，但不等同生产Redis多进程故障注入。PG通过指隔离真实PostgreSQL子场景。下表“部分验证”明确保留原场景尚未覆盖的条件，不把源码存在或参考仿真算作通过。线上效果、灰度、真实付费上游调用未执行。

| 标记 | 实际源码 |
|---|---|
| S1 | backend/internal/scheduling/policy.go、runtime.go：分层、权重、pin/fill_first、全慢选择 |
| S2 | backend/internal/scheduling/ledger.go、health.go、redis_store.go：预算、健康、共享SWRR/探测/重试 |
| S3 | backend/internal/service/controlled_scheduling*.go、gateway_scheduling.go、openai_controlled_scheduling.go：入站账本与适配器准入 |
| S4 | backend/internal/scheduling/postgres_*.go、backend/internal/handler/admin/scheduling_handler.go：权威gate/控制/票据/只读观测 |
| S5 | backend/internal/service/openai_ws_controlled*.go、openai_ws_v2_passthrough_adapter.go：逐轮准入/取消/提交/结算 |
| U1 | frontend/src/views/admin/SchedulingView.vue、账号控制/trace/stats组件、frontend/src/utils/scheduling.ts |

## 已观察测试证据

| 引用 | 实际测试 |
|---|---|
| P1 | core_test.go: TestPolicyValidationAndSpecificProfiles、TestPriorityWeightsModesAndCapacity |
| P2 | core_test.go: TestLedgerGlobalTierDeadlineAndCommit |
| P3 | core_test.go: TestHealthCooldownAndRecovery、TestRecoveryShareUsesConfiguredTarget、TestAllSlowCensoredEvidence、TestFailureDomainExclusionAndFreshRecoveryEvidence、TestHealthSeparatesSlowAndFailureWindows、TestRecoveryRequiresSuccessfulTerminalAndBacksOff、TestObserveOnlyStillProtectsAgainstFailures |
| P4 | core_test.go: TestRedisSWRRIsSharedAndReservationsRefund、TestRedisConcurrentSWRRAndNoLocalFallback、TestSharedRetryBudgetCountsActualDispatch、TestUnknownProbeAndRedisHealth、TestCanRetryIsReadOnlyAndNoProfileDoesNotThrottle、TestSelectionExpiryCompensatesAndCannotCommit、TestPinAndRetryDoNotConsumeOrdinaryAllocation |
| D1 | postgres_integration_test.go: TestPostgresControlIntegration；下表斜线后为PG子用例名 |
| D2 | backend/internal/handler/admin/scheduling_handler_test.go: TestScheduling* |
| G1 | controlled_gateway_selection_test.go: TestControlledGatewayEligibilityPreservesHardGates、TestControlledGatewayEligibilityIgnoresTPSMetadata |
| W1 | openai_ws_controlled_passthrough_test.go: TestControlledPassthrough* |
| W2 | openai_ws_controlled_test.go: TestOpenAIWSControlled*及既有owner回归 |
| U1 | 前端scheduling新增6个Vitest文件43项；账号页/菜单/路由/i18n既有21文件159项 |
| E1 | controlled_scheduling_explain_integration_test.go: TestExplainRealPostgresRedisIsPureAndPredictsNextSelection；explain unit tests、scheduling/introspection_test.go及repository并发只读测试 |
| H1 | HTTP_INTEGRATION_02：TestControlledRealStoresAndHTTP真实PG/Redis+本地HTTP/SSE服务12个子场景，含双服务实例7:3、A1→A2→B1、暂停双顺序、跨节点强停/恢复、语义时钟、429、截断与非流式 |
| R1 | controlled_scheduling_independent_review_test.go四项审查：完整无参Chat工具、PG记录失败不算发送、headers不延长已预留截止、过期D不重启；REVIEW_DISPATCH_FIXED中四项均PASS |
| R2 | dispatch-receipts-race.log：miniredis及真实Redis六类commit/退款/过期回执场景、核心策略与PG控制全部-race PASS |
| R3 | BACKEND_FULL_UNIT_FINAL及BACKEND_CRITICAL_RACE_FINAL：真实PG记录失败、Redis durable prepare后故障均不计实际派发，TestControlledStopReasonPreservesAttemptOutcome及四项独立审查实际PASS |
| G2 | policy-adapter-review-race.log：controlled legacy/协议错误身份/禁同号补发与自动语义降级/OAuth保护、service及handler定向-race回归均PASS |
| R4 | fallback-preview-baseline-executed.log真实PG/Redis复现假后备5失败1成功；fallback-preview-fixed.log修复后-race通过，同层/总次数/超时上限/共享token使用后的资格联合检查 |
| R5 | retry-json-baseline.log复现部分JSON配置误关默认；retry-json-fixed.log修复后-race通过，省略/空/null/部分/显式false与0/Go构造/坏JSON及真实PG roundtrip |
| R6 | request-metadata-baseline.log同命令22个请求档子项FAIL；request-metadata-fixed.log修复后PASS，最终全量及critical race逐项确认22个子项、canonical aliases和原生工具ReplaySafe保护PASS |
| R7 | legacy-owner-control-baseline.log真实PG/Redis复现暂停owner被B替换且实际发送；同输入overlay六项PASS，legacy-owner-control-frozen-race.log扩展10个真实存储子场景及既有owner回归共20个顶层PASS |
| B1 | BACKEND_FULL_UNIT_FINAL.command.json：全后端go test -tags=unit ./...实际exit0，11569个顶层PASS、38个顶层SKIP、62个包PASS；执行于最后owner窄修之前 |
| B2 | BACKEND_CRITICAL_RACE_FINAL.command.json：scheduling/service/handler/admin/repository五包定向-race实际exit0，105个顶层PASS、1个顶层SKIP；执行于最后owner窄修之前 |
| B3 | BACKEND_OWNER_DISPATCH_FINAL.command.json：最后owner修复后service/handler两包-race实际exit0，54个顶层PASS，无SKIP/FAIL；BACKEND_FINAL_FROZEN_BUILD实际exit0，FINAL_SOURCE_VERIFICATION确认前后源码哈希不变 |

实际执行摘要（所有路径位于隔离候选）：

- GOMAXPROCS=2 go test -race -p 1 -count=1 ./internal/scheduling：PASS，1.309s，17个核心顶层测试，包含miniredis Lua。
- go test -p 1 -run TestControlledGateway -count=1 ./internal/service：PASS，0.028s。
- pg-control-complete.log：go test -race -p 1 ./internal/scheduling -run TestPostgres -count=1 -v，以及go test -p1 ./migrations -count=1，整个命令exit0；16个真实PG子场景+4个单元测试，包含stats/trace/usage持久水位。
- pg-control-trace-check.log与ws-control-check2.log保留较早失败：各目标子测试曾PASS，但前者末迁移checksum、后者后续vet失败；不能把旧复合命令整体标成成功。最终PG日志已复验修复。
- W1最终10项及现有TestPassthrough回归均-race通过，20.898s；完整日志ws-passthrough-final.log，逐turn usage绑定回归已通过。W2新增3项及既有owner9项已观察PASS。
- 前端vue-tsc与ESLint通过；frontend-protocol-final.log最新6文件43项PASS，涵盖global策略/首字档继承、六种canonical协议单独提交、空transport与anthropic别名。既有账号页/路由/菜单/i18n21文件159项PASS。测试使用模拟后端，不能替代真实浏览器/API联调；继承修复后的生产构建exit0，1091模块、30.87s，见frontend-inheritance-review-final.log；已有大包/Browserslist提示保留。
- explain-real-stores-complete.log整个命令exit0：真实PostgreSQL 18与Redis 8.4环境下，解释前后7张PG表完整行JSON和每个Redis键DUMP相等，下一次真实SWRR选择与解释预测相同；同次命令中的profile继承、unknown、pin、容量、凭据族暂停、恢复阶段和只读过期预留/忙探针测试通过。早期explain-final-race.log的中间符号编译失败保留；随后explain-final-verified.log已整链-race PASS（scheduling 1.270s、service 1.848s），包括真实Redis只读预测和真实PG/Redis解释纯度，不能再标记为未运行。

上述日志目录：/xy2/artifacts/iq-candy-20260927/scheduling-implementation-20260927/。未生成独立日志的测试保留工具执行结果，由主执行器证据账本收口。

## 79项逐条映射

每行状态均为“部分验证，原完整业务场景待补”；“—”表示没有专门已观察回归。不从核心测试数推算完整验收通过数量。

| # | 原始用例ID | 原验收目标 | 实现 | 已有测试 | 待补验证 |
|---:|---|---|---|---|---|
| 1 | pin_available | 所有请求只调用A | S1 | P1、P4 | 核心候选规则已测；各协议实际派发及真实调用计数待验 |
| 2 | pin_unavailable | 明确失败，B调用数0 | S1 | P1、P4 | 核心候选规则已测；各协议实际派发及真实调用计数待验 |
| 3 | strict_priority | 新普通分配备层计数0 | S1 | P1、P4、H1/strict_priority_shared_7_to_3 | 真实PG/Redis+HTTP的同进程双服务实例20次14:6且低层为0；全部供应商路径和生产多进程仍待验 |
| 4 | capacity_modes | 行为与配置一致，原因可解释 | S1、S2、S4 | P1、D1/hard_capacity_keeps_unknown | 溢出/容量核心已测；多组多模型并发及限时排队全链待验 |
| 5 | swrr_7_3 | 确定性分配7000/3000 | S1、S2 | P4、H1/strict_priority_shared_7_to_3 | miniredis双Runtime100次70:30；真实PG/Redis+HTTP双服务实例20次14:6；原矩阵10000次7000/3000与独立多进程待验 |
| 6 | zero_weights | 0不接新普通分配；全零明确错误 | S1 | P1、P4 | 核心候选规则已测；各协议实际派发及真实调用计数待验 |
| 7 | manual_share | 软评分不偷偷改目标权重 | S1 | P1、P4、H1、G1 | 真实存储双实例7:3、TPS不入候选已测；真实供应商长期流量份额和多进程负载混合待验 |
| 8 | affinity_modes | 分别验证关闭、层内重绑、既有会话保留 | S1、S3 | P1、W2 | 最终契约默认软粘性不改路；旧三模式需按最终契约收口，显式保会话比例未独立验收 |
| 9 | protocol_owner | 显式错误且续接状态完整 | S3、S5 | W2、R7 | WS owner及切回legacy后人工暂停不能替换强owner已测；注册会话排空仅沿原owner；pin冲突与全部协议安全重建待验 |
| 10 | group_override | 各组独立分配，全局容量不超卖 | S1、S2、S4 | P1、D1/hard_capacity_keeps_unknown | 溢出/容量核心已测；多组多模型并发及限时排队全链待验 |
| 11 | retry_and_stream | 允许的重试单独记账，流已输出不重放 | S2、S3、S5 | P2、W1、W2、H1、G2 | 真实HTTP/SSE的三次总账、首字超时后仅再一次、下游commit后禁换号已测；全部供应商和所有转换组合仍待验 |
| 12 | multi_instance | 共享份额可核对，预留可回收，无超卖 | S2、S4 | P4、D1/pause_dispatch_race、H1、R2 | 真实Redis双服务实例共享7:3、跨实例force/resume及回执补偿已测；独立进程崩溃、Redis状态丢失和网络分区待验 |
| 13 | policy_version | 请求版本固定；使用最后有效配置或明确失败 | S3、S4 | D1/policy_compare_and_swap、D2、U1、R7 | CAS、legacy映射及回退后owner暂停/排空门继续生效已测；在途请求跨版本固定和跨版本数据库回滚待验 |
| 14 | protocol_regression | owner、模型能力、错误和流终态均正确 | S2、S3、S5 | P2、W1、W2、H1、G2 | HTTP/SSE真实本地网络、WS adapter、Bedrock解码/错误保真/禁止同号补发定向回归已测；全部供应商端到端组合待验 |
| 15 | hard_eligibility | 所有策略拒绝不合格候选 | S3 | G1、G2、E1 | TPS元数据不改硬准入、Explain复用供应商硬门槛、OAuth凭据保护已测；真实供应商授权/额度联测待验 |
| 16 | explain_no_effect | 账号调用、并发槽、TTL及SWRR余额均不变 | S1、S2、S4、U1 | P4、D2、E1、H1、U1 | 真实PG7表完整行与Redis每键DUMP不变、下一SWRR一致；最终-race解释链通过；任意并发控制组合和TTL边界持续故障注入待验 |
| 17 | legacy_rollback | 策略恢复，owner保留，审计完整 | S3、S4 | D1/policy_compare_and_swap、D2、U1、R7 | 真实PG/Redis切回legacy后暂停owner不派发B，注册会话仍可沿owner排空，未人工控制的legacy既有行为保持；跨版本数据库回滚与完整审计链待验 |
| 18 | smart_v2_model_profiles | 各自门槛和统计隔离，缺省仅观测 | S1、S2 | P1、E1、G2、U1、R6 | canonical协议/别名、profile继承/unknown、UI六协议和Gemini/Anthropic/OpenAI原生思考档22子项已测；真实提供方各请求档连续采样分布待验 |
| 19 | smart_v2_ttft_only_bad | 按首字健康窗口降级 | S2 | P3 | 纯Go窗口已测；在线连续请求触发/恢复待端到端 |
| 20 | smart_v2_single_soft_bad | 记样本，不立即全账号熔断 | S2 | P3 | 纯Go窗口已测；在线连续请求触发/恢复待端到端 |
| 21 | smart_v2_local_delay_attribution | 不误伤上游健康 | S2、S3 | P3 | Excluded样本不改变健康已测；真实背压/排队/取消组合待验 |
| 22 | smart_v2_degraded_best_effort | 仅按首字偏离与优先级容差选择，不读TPS | S1、S2 | P3 | 全慢容差/fail_fast/OPEN拒绝已测；真实硬限额解除/有限等待待验 |
| 23 | smart_v2_degraded_mode_override | 遵守管理员显式例外策略 | S1、S2 | P3 | 全慢容差/fail_fast/OPEN拒绝已测；真实硬限额解除/有限等待待验 |
| 24 | smart_v2_hard_all_unavailable | 有限等待或明确失败，禁止绕过 | S1、S2 | P3 | 全慢容差/fail_fast/OPEN拒绝已测；真实硬限额解除/有限等待待验 |
| 25 | smart_v2_cooldown_not_recovery | 只能获探测资格，不能变健康 | S2 | P3 | Go时间推进、连续成功与驻留已测；真实时间多节点恢复待验 |
| 26 | smart_v2_recovery_evidence | 按样本/驻留进入恢复，不检查速度或输出token数 | S2 | P3 | Go时间推进、连续成功与驻留已测；真实时间多节点恢复待验 |
| 27 | smart_v2_cross_priority_recovery_cap | 全池份额10%/30%上限，不突变100% | S1、S2 | P3 | 1%正常份额的10%恢复为全池0.1%已测；多恢复账号跨层共享额度待验 |
| 28 | smart_v2_recovery_dwell_and_sample | 同时满足驻留与样本再晋级 | S2 | P3 | Go时间推进、连续成功与驻留已测；真实时间多节点恢复待验 |
| 29 | smart_v2_recovery_failure_backoff | 退回冷却且受限重试 | S2 | P3 | Go时间推进、连续成功与驻留已测；真实时间多节点恢复待验 |
| 30 | smart_v2_restored_manual_weight | 回到原始7:3，不补故障期欠账 | S1、S2 | P3、P4 | 正常SWRR/恢复状态分别已测；完整恢复后7:3周期及不补欠账组合待验 |
| 31 | smart_v2_retry_before_output | 取消后按剩余总预算重试，最多预算次数 | S2、S3 | P2、H1/headers_timeout_counts_and_keeps_unknown_capacity、R1 | 真实HTTP首字超时取消、总账2次上限和换号已测；全部协议交替取消/重放与真实提供方确认待验 |
| 32 | smart_v2_no_midstream_splice | 不拼接另账号回答，保护协议状态 | S3、S5 | P2、W1、H1/downstream_commit_and_nonstream_timing | 真实HTTP下游commit禁重放、SSE截断归unknown已测；真实服务端工具副作用及全部转换路径拼接保护待验 |
| 33 | smart_v2_pre_output_budget | 首次输出前总预算不重置，不误作全流时长 | S2、S3 | P2、H1、R1 | headers不延长已预留650ms截止、过期D不重启及推理首字后长流不被T截断已测；多适配器交替排队组合待验 |
| 34 | smart_v2_shared_probe_budget | 共享探测令牌与恢复份额，不每台独立超发 | S2 | P3、P4 | UNKNOWN/过期/共享probe核心已测；真实多进程等待与恢复待验 |
| 35 | smart_v2_stale_and_global_bad | 未知受限试探；冻结学习，不自动放宽底线 | S2 | P3、P4 | UNKNOWN/过期/共享probe核心已测；真实多进程等待与恢复待验 |
| 36 | v4_retry_peer_before_tier | 只尝试A1/A2，不调用下级 | S1、S2、S3 | P1、P2、H1/three_actual_attempts_same_tier_first | 真实HTTP 503调用链A1→A2→B1已测，同层优先明确；同层A2成功时B1为0的独立完整场景待补 |
| 37 | v4_retry_next_tier | 预算允许时进入B1 | S1、S2、S3 | P1、P2、H1/three_actual_attempts_same_tier_first | 真实HTTP 503后第三次到B1且总派发3已测；B1成功挽救及所有转换路径组合待补 |
| 38 | v4_retry_tier_cap | 按设置降级或终止，记录tier_attempt_limit | S1、S2、S3 | P1、P2、H1/three_actual_attempts_same_tier_first | 真实HTTP默认同层最多2后下层及总尝试3已测；exhaust_same_tier实际链与各停止原因文案逐项待验 |
| 39 | v4_retry_no_repeat | 所有失败账号累计排除，不回到旧高层 | S1、S2、S3 | P1、P2、H1/three_actual_attempts_same_tier_first | 真实HTTP三次使用不同账号A1/A2/B1，第四次拒绝；更长自定义预算与动态恢复不折返组合待验 |
| 40 | v4_retry_shared_budget | 初次+所有重试共享次数和deadline | S1、S2、S3 | P1、P2、H1、R1、G2 | 真实HTTP首调+换号同账、超时后额外一次、已过deadline不重启和禁止adapter内部重发已测；嵌套OAuth/转换全路径仍待验 |
| 41 | v4_retry_terminal | 停止，不再扫描账号 | S2、S3、S5 | P2、W1、H1、G2 | 下游commit拒绝继续派发、明确停止错误身份/handler映射、WS错误延迟commit已测；全部请求级策略拒绝与owner组合待验 |
| 42 | v4_retry_shared_limit | 跳过同限制域，尊重Retry-After | S1、S2、S3 | P3、H1/429_skips_shared_credential_family、G2 | 真实HTTP429及Retry-After使同凭据族账号跳过，独立下层补位已测；真实供应商共享项目限额作用域仍待验 |
| 43 | v4_tps_complete_invariance | 正常、降级、兜底、恢复、暂停决策均不变 | S1、S2、S3 | G1、P1、G2 | TPS硬准入不变，adapter不擅自改模型/思考/上下文的定向回归已测；全健康/恢复状态的供应商端到端矩阵待验 |
| 44 | v4_pause_dispatch_race | 按权威门顺序决定已准入集合，旧缓存不得放新ticket | S4 | D1/pause_dispatch_race、H1/pause_wins_before_gate_no_call_or_budget、H1/gate_wins_pause_drains_and_settles_once | 真实PG竞争和HTTP gate前后两顺序已测；跨服务独立进程旧缓存及长时发包边界待验 |
| 45 | v4_pause_unsent | 释放一次并重选，不算上游attempt | S2、S3、S5 | P4、W1、H1/pause_wins_before_gate_no_call_or_budget、R1、R2、R3 | 真实PG暂停先行零调用/零尝试、PG记录失败及Redis durable prepare后故障不计实际派发、回执退款已在全量及-race通过；独立进程在提交间崩溃的完整对账仍待验 |
| 46 | v4_pause_inflight | 继续原响应至终态，不因暂停重放或拼接 | S4、S5 | D1/pause_and_settlement、W1、H1/gate_wins_pause_drains_and_settles_once | 真实PG gate先行后暂停，获准请求继续且只结算一次；跨长时HTTP/SSE/WS完整排空压力组合待验 |
| 47 | v4_pause_ws | 本轮完成，下轮重新准入 | S5 | W1、W2 | 当前轮/下一轮及取消桥已测；真实双连接客户端全链待验 |
| 48 | v4_pause_strong_owner | 保留owner，安全重建或有界会话排空，否则明确提示 | S3、S4、S5 | W2、D1/bounded_session_drain、R7 | 真实存储下legacy回退、逻辑账号/凭据族暂停、未登记会话及缺请求上下文均阻断替代派发，已登记会话可沿原owner排空；所有不透明状态安全迁移未承诺 |
| 49 | v4_pause_session_bounds | 仅快照内且未越界的会话例外获准 | S4 | D1/bounded_session_drain、D1/session_candidate_is_read_only、U1、R7 | PG会话快照/轮次与legacy路径登记会话/未登记/缺上下文/硬健康阻断已测；协议截止瞬间并发追加待验 |
| 50 | v4_pause_manual_control | 不取消手动暂停，兜底不得绕过 | S2、S4 | D1/pause_and_settlement、W2、R7 | 人工控制与健康分层、切回legacy及高级路由不能绕过暂停强owner已测；恢复流程并发撞人工暂停待联测 |
| 51 | v4_pause_resume_epoch | 旧epoch不得覆盖新状态或误关新连接 | S4 | D1/epoch_conflict_and_old_settlement、D1/force_stop_survives_resume_and_is_scope_fenced、H1/force_then_resume_still_cancels_old_ticket_on_other_node | 真实PG旧epoch与另一服务实例force→resume仍取消旧ticket已测；独立进程延迟PubSub/真实网络连接矩阵待验 |
| 52 | v4_pause_deadline | 默认告警继续排空，强停须显式 | S4、U1 | U1 | 5分钟告警基于真实updated_at且不发控制命令；前端新增回归，长流原请求期限联测待验 |
| 53 | v4_pause_uncertainty | 显示未知，不能伪报完全排空 | S4 | D1/lease_expiry_and_force_stop、D1/hard_capacity_keeps_unknown、H1 | 真实HTTP超时/语义流截断转unknown并保留容量已测；进程崩溃和网络分区全链待验 |
| 54 | v4_pause_settlement | usage保留、结算与释放幂等 | S4、S5 | D1/pause_and_settlement、W1、H1/gate_wins_pause_drains_and_settles_once | PG幂等结算/usage水位、HTTP一次结算、WS逐turn绑定与既有计费回归已测；供应商实际计费对账待验 |
| 55 | v4_pause_scope | 只作用于选定范围，母/影分别验收 | S4 | D1/family_and_logical_scope、R7 | PG母/影范围及legacy强owner逻辑账号/凭据族暂停和排空路径已测；独立进程跨缓存传播全链待验 |
| 56 | v5_event_classification | 跨HTTP/WS/转换路径只以语义内容记首字；完整无参数工具调用不得误判为空占位 | S3、S5 | W1、H1、R1、G2 | 真实SSE心跳不算首字、推理/正文分离、完整无参数Chat工具修复及Bedrock解码定向回归已测；全部提供方事件变体待验 |
| 57 | v5_reasoning_vs_answer | 分别记录首语义与首正文，推理已提交后不透明重放 | S3、S5 | W1、H1/semantic_reasoning_stops_first_output_timer | 真实SSE推理先到并停止首字T、正文更晚且分别记录已测；全部adapter推理commit与失败交叉路径待验 |
| 58 | v5_two_clocks | 账号指标排除本地排队；请求整体指标包含所有等待 | S3、U1 | U1、H1、R1 | 真实HTTP分别记录上游首事件/语义/正文与下游commit，字段缺测保持空；排队+退避+多失败整体等式仍待验 |
| 59 | v5_nonstream_metric | 完成时间不伪装成首token时间 | S3 | H1/downstream_commit_and_nonstream_timing | 真实HTTP非流式完成后FirstSemanticMS仍为nil、下游响应才commit已测；各非流式转换协议同口径待验 |
| 60 | v5_health_vs_timeout | 保留本次请求，累计健康证据而非立即重放 | S2 | P3 | 纯Go窗口已测；在线连续请求触发/恢复待端到端 |
| 61 | v5_censored_timeout | 记TTFT至少12秒与违例，不填精确12或丢弃 | S2、S3 | P3、H1/headers_timeout_counts_and_keeps_unknown_capacity | 真实HTTP仅headers超时记截断并保留unknown容量，核心timeout惩罚T已测；长期日志下界与全模型采样联测待验 |
| 62 | v5_clipped_timeout | 标记预算受限，不凭不足8秒的观察判账号慢 | S2、S3、S5 | P2、W1、R1 | clipped与真实首字超时区分、过期整体D立即取消且clipped=true已测；全部adapter真实健康归因组合待验 |
| 63 | v5_model_effort_context | 按显式profile比较，样本不足回退/观察 | S1、S2 | P1、E1、G2、U1、R6 | 模型profile分桶/global继承/unknown/canonical别名及原生effort与thinking budget独立表达、无配置default、不从无效文本推断已测；全部真实模型请求档连续分布待验 |
| 64 | v5_quick_third | 总账与时间/令牌允许时第三次可救回 | S2、S3 | P2、P4、H1/three_actual_attempts_same_tier_first | 真实HTTP允许快速失败后的第三次B1，实际调用计数3已测；第三次成功挽救和令牌竞争组合待验 |
| 65 | v5_post_timeout_cap | 首次首字超时后最多再派发1次，计数不重置 | S2、S3 | P2、P4、H1/headers_timeout_counts_and_keeps_unknown_capacity | 真实HTTP首字超时后只再派发一次，第三次拒绝且总Attempts=2已测；OAuth/WS多层嵌套组合待验 |
| 66 | v5_third_no_time | 不派发第三次，解释insufficient_effective_window | S2、S3 | P2、P4 | 预算核心已测；合法后备/令牌余量与实际生成调用链联测待验 |
| 67 | v5_reserve_viable | 只预留一次后备，不能平均切三段 | S2、S3 | P2、P4、R1、R4 | headers前后保持一次后备预留650ms已测；真实PG/Redis pending尝试后仍合法的下层保留；独立多进程容量竞争待验 |
| 68 | v5_no_fake_reserve | 不为空后备缩短当前窗口；当前会耗尽最后一枚重试令牌也不预留第三次 | S2、S3 | P2、P4、R4 | 真实PG/Redis同层次数耗尽、总次数耗尽、pending后超时上限、MaxAfterTimeout=0和当前重试用尽共享token均不假留后备，修复后-race PASS；生产多进程竞争待验 |
| 69 | v5_oauth_global_cap | 再次发送生成请求均消费同一attempt总账 | S2、S3 | P2 | 统一总账代码已接；OAuth刷新/同号/换号嵌套实际发送计数待验 |
| 70 | v5_deadline_all_adapters | 沿用入站绝对deadline且使用单调时钟 | S2、S3、S5 | P2、W1、W2、H1、R1 | HTTP headers后不延长已定截止、过期D不重启、WS每轮独立账本已测；HTTP/WS/转换/暂停重选交替完整场景待验 |
| 71 | v5_global_retry_bucket | 共享重试额度封顶，初始请求独立，存储故障有保守上限 | S2 | P4、R2、R4 | 真实Redis回执退款、已发确认不可退款、当前派发后token余量预览已测；独立多进程故障风暴及Redis状态丢失恢复待验 |
| 72 | v5_shared_retry_after | 跳过整个受限作用域，独立账号可按策略补位 | S1、S2、S3 | P3、H1/429_skips_shared_credential_family、G2 | 真实HTTP同凭据族429跳过与已知team/model failure-domain定向回归已测；提供方实际共享项目配额验证待验 |
| 73 | v5_unsafe_replay | 不能仅因writer未输出便自动重放 | S3 | P2、R6 | ReplaySafe=false核心拒绝和原生工具声明保持不可安全重放已测；已执行服务端工具的提供方实测待验 |
| 74 | v5_credential_refresh | 刷新本身独立记录；生成重发仍受总次数上限 | S2、S3 | P2 | 统一总账代码已接；OAuth刷新/同号/换号嵌套实际发送计数待验 |
| 75 | v5_metric_migration | 带metric_version，旧数据不直接混入新健康窗口 | S2、S3、S4 | P1、D1/request_trace_redaction_and_unobserved_values | 新指标版本/白名单已有；旧历史不混入健康窗口迁移联测待验 |
| 76 | v5_timeout_config_migration | 前后端校验与兼容迁移一致，不静默忽略无效值 | S1、U1 | P1、U1、R5 | 秒转毫秒/canonical协议/JSON缺省与显式false/0、真实PG往返已测；既有生产配置升级后每模型profile匹配联测待验 |
| 77 | v5_commit_atomicity | 一旦语义提交不得再派发备选，终态与计费只结算一次 | S2、S3、S4、S5 | P2、D1/force_stop_settlement_lock_order、W1、H1、R1、R2 | 真实HTTP下游commit禁止再派发、PG只结算一次、回执确认后不退款、WS重复finish已测；flush/timeout/cancel多进程压力竞态待验 |
| 78 | v5_no_tps_no_auto_downgrade | TPS不改变选路，不私自改模型/effort/上下文 | S1、S2、S3 | G1、P1、G2 | TPS不参与选路、受控adapter禁止自动降模型/思考/上下文及同号重发定向回归已测；所有供应商端到端矩阵待验 |
| 79 | v5_budget_explain | 日志与界面返回具体原因及已用/剩余预算 | S3、S4、U1 | D2、U1、E1、H1、R3 | 真实Explain纯度/下一SWRR预测、trace/stats分账、缺测展示及StopReason保留attempt outcome已测；每一种停止原因通过真实请求UI完整覆盖待验 |

## 新增收口检查与上线边界

1. 独立traffic_weight、不从load_factor换算、零权重不得被retry/probe/fallback复活；前后端及所有入口要联合验证。
2. PG/Redis故障时新精准派发明确失败、已有流继续；真实依赖故障注入仍需补足，不能只凭错误stub宣称完成。
3. 多恢复账号共享10%/30%/100%正常目标份额，不逐节点重复发放或补故障欠账。
4. stats只计持久sent_at的真实派发，ordinary_first与retry/probe/pin/owner/fallback单列；零分母null，未观测不是0。
5. trace/stats/explain不消费容量/分流/探测/会话；metrics白名单不保存凭据或完整请求。
6. usage持久成功才ack同一attempt；不同WS turn不借第一轮ticket。未知远端状态继续占容量。
7. 升级默认legacy、模型阈值默认全零仅观测；不向所有模型写统一秒数。
8. 默认排空5分钟只告警、不自动强停；远端停止与结算确认分别显示。

候选不部署、不切生产、不调用真实上游。真实Redis与同进程双服务实例已测；测试分组灰度前仍需补齐独立多进程崩溃/状态丢失、全部供应商适配器、flush/timeout/cancel压力竞态和依赖持续故障注入；再按模型比较预算内成功率、整体首字P50/P95、响应完整率、调用放大率及分流偏差。样本不足不得宣布体验提升。

四角色archive/diff/verification/rollback由主执行器收口；本文件不复用旧IQ事务基线或回滚结论，也不把待验项隐藏为通过。

补充日志：ws-usage-regression.log整体exit0，包含service层既有WS/bridge/RecordUsage及新usage snapshot/10分支测试。

前端证据沿时间保留：frontend-scheduling-final.log为39项及27.03s构建；frontend-inheritance-review-final.log与frontend-inheritance-review-api.log合计41项及30.87s构建；frontend-protocol-final.log新增协议收口后43项、vue-tsc及ESLint均通过。WS最终37个顶层测试通过，其中10个为新增控制wrapper测试。

## Explain补充验证边界

- 真实PG/Redis集成测试包含unknown票据、凭据族暂停、正在等待的SWRR预约和过期Redis并发记录；读取前后accounts、scheduler_outbox、scheduling_policies、scheduling_controls、scheduling_attempts、scheduling_owner_sessions、scheduling_session_grants完整内容相同。
- Explain单元测试覆盖本组策略缺失时group 0默认继承、独立profile继承、unknown上下文档、pin、max(PG, Redis)容量、family控制、恢复阶段/good_streak和真实目标/有效份额。Redis只读检查覆盖过期SWRR预约在预测中的补偿和繁忙probe跳过。
- 原始schema、核心实例和真实协议网络测试分开归因；这里未将参考仿真升级为验收，也未宣称所有供应商联调、并发TTL竞态或生产灰度完成。

协议收口最终构建：frontend-protocol-build.log完整退出0，Vite 28.52s。frontend-protocol-final.log完整退出0，vue-tsc、6文件43个Vitest与定向ESLint通过。界面展示六种canonical协议及明确HTTP端点，空transport仅共用匹配不合并样本；原anthropic请求档展示/提交为messages。

## HTTP与独立审查的实际观察闭环

- HTTP_INTEGRATION_02.command.json记录exit_code=0；12个真实PG/Redis+本地HTTP/SSE子场景及6种语义分类通过，service 4.951s。这里的HTTP上游是本地测试服务，不是付费供应商；双节点是同进程两套服务实例，不冒充独立多进程验证。
- REVIEW_HEADER_BASELINE_02真实执行失败：headers在400ms到达后延长原650ms截止，750ms观察时仍未取消。这是已观察缺陷，不再列作仅推断或编译受阻。
- REVIEW_DISPATCH_FIXED中四项独立审查测试均PASS：无参Chat工具语义、PG记录失败不计send、headers不延长后备窗口、过期整体预算不重启；HTTP前11项PASS，但Explain测试fixture缺少新的只读依赖导致末项FAIL，因此整个命令exit_code=1，不能记为整体通过。
- explain-final-verified.log整链-race PASS；dispatch-receipts-race.log的真实Redis六类回执场景及PG/核心测试PASS；policy-adapter-review-race.log的service 2.035s与handler 17.228s均PASS。早期失败日志保留作为修复过程，不能覆盖或借用旧IQ事务结果。
- R3：早期REVIEW_DISPATCH_FINAL仍保留exit_code=1及Runtime.CanRetryAfterDispatch中间符号编译错误；后续BACKEND_FULL_UNIT_FINAL和BACKEND_CRITICAL_RACE_FINAL均exit_code=0，已实际运行并通过TestControlledPrepareFailuresNeverCountActualDispatch的postgres_record_failure与redis_failure_after_durable_prepare两子场景、TestControlledStopReasonPreservesAttemptOutcome及四项独立审查。早期未运行到断言不代表最终仍未验证；最终通过也不抹除早期失败。
- 假后备审计R4保留真实失败与修复后-race日志；六个PG/Redis选择子场景包括合法下层正例。纯ledger下降不折返与PG prepared metrics→not_sent不计实际流量同时通过，不将这些有限场景外推为所有生产重试竞态已完成。
- API默认审计R5：partial retry JSON曾错误关闭cross_tier/reserve_fallback/首字超时后备，真实失败日志保留；独立JSON反序列化修复后-race验证缺省采用DefaultRetryPolicy、显式false/0不被覆盖、原Go构造不变和真实PG持久往返。

## 最终执行范围与冻结边界

- BACKEND_FULL_UNIT_FINAL.command.json实际exit_code=0：全后端unit命令包含11569个顶层PASS、38个顶层SKIP、62个包PASS，无顶层FAIL。SKIP原样保留，不计为通过。BACKEND_CRITICAL_RACE_FINAL.command.json实际exit_code=0：五包定向-race，105个顶层PASS、1个顶层SKIP。两套均执行于最后legacy owner窄修之前，不能宣称其直接覆盖之后的最终源码。
- 请求档修复保留同命令前后证据：request-metadata-baseline.log的22个原生协议请求档子项实际FAIL，另有canonical constructor与原生工具安全性顶层FAIL；request-metadata-fixed.log通过，之后全量unit和critical race逐子项再确认PASS。缺省/invalid/unknown不伪造思考强度，Gemini与Anthropic显式budget保持独立维度。
- 最后owner窄修的直接缺陷证据：legacy-owner-control-baseline.log中account_pause与unregistered_session两分支均选择B并实际派发一次，family_pause也未在候选阶段返回应有阻断；原六项为3 FAIL / 3 PASS。同输入overlay修复后六项全部PASS，见legacy-owner-control-overlay-fixed.log。
- 最后owner修复之后，legacy-owner-control-frozen-race.log实际-race通过20个顶层测试，其中TestControlledLegacyRollbackOwnerGuard含10个真实PG/Redis子场景：逻辑账号/凭据族暂停与排空、未登记会话、正常legacy、排空仍受硬健康限制、未人工控制时既有健康fallback、高级可移动路由暂停、缺请求上下文。原owner与WS回归一起通过；此定向结果不扩大为重新执行全部后端unit。
- 最终冻结后BACKEND_OWNER_DISPATCH_FINAL两包-race实际exit_code=0，54个顶层PASS、无SKIP/FAIL，service 8.181s、handler 1.193s；BACKEND_FINAL_FROZEN_BUILD实际exit_code=0，stdout/stderr均为空。FINAL_SOURCE_VERIFICATION.json记录complete=true、race_exit_code=0、build_exit_code=0、source_unchanged=true；验收文档收口时再次核对全部3280个Go/SQL/mod/sum文件与该清单完全一致。生产二进制SHA256为baf6cf8beb877a60435e6929038f4cf98ac7f1ecd5ede2ef98c88730cbeb0172。
- 全量unit数量不等于79项原业务验收全部通过；最终冻结后完成相关定向回归与生产构建，没有把owner修复前的全量结果改写成修复后重跑。生产多进程故障注入、全部供应商端到端联调和线上灰度指标仍按上表保留待验。
