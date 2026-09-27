# Controlled scheduling core

This package is independent of service/handler types. The production adapter must
supply hard eligibility and authoritative capacity; these algorithms never read
TPS, cost scores, soft affinity, or the old load factor.

## Policy and selection

- Policy account overrides expose **traffic_weight** (Go field Weight). A missing
  override inherits account priority and weight 1; explicit zero receives no
  automatic business, retry, or recovery traffic. Lower priority numbers win.
- SWRR, pin, and fill_first are explicit modes. Pin without fallback never leaves
  the selected account. Pin/owner traffic, ordinary traffic, and retry allocation
  use independent Redis balances. Capacity overflow is independent of health.
- Latency profiles use milliseconds with 0 < R < H < T <= D and 0 < M <= T.
  ResolveProfileForTransport selects model/request-class overrides. An absent or
  all-zero profile is observe-only for latency, **not** for errors.
- Health keys isolate model, reasoning, context bucket, transport, and H/R/T
  configuration. Three slow observations among the latest five comparable
  observations in 120 seconds degrade an account, without requiring all five.
  Three attributable failures among the latest five terminal attempts in the
  same window open the breaker. Slow and failure counts are separate.
- Initial cooldown is 30 seconds. Failed recovery increases it to 60/120/240/300
  seconds; a later upstream Retry-After wins. Unexpired OPEN accounts cannot enter
  the all-degraded fallback. Observe-only still samples failures, opens on 3/5,
  and recovers on complete successful requests; it never applies TTFT thresholds.
- Recovery requires three **complete successful** comparable requests at or below
  R, then 10%/30%/100% of the account's normal configured pool share, with three new
  successful samples and 30 seconds per ramp stage. Without latency configuration,
  complete success replaces the R check. A progress chunk is not complete success;
  a later response.failed must set AttributableFailure. Stale samples cannot
  advance a stage. With no healthy recipient, explicit bounded-best-effort handles
  traffic under capacity limits instead of deadlocking on percentage caps.
- UNKNOWN bootstrap allows one probe per account. With a healthy recipient,
  canaries also share a pool-wide concurrency lease. No paid synthetic probes run.
- All-slow ranking is a conservative mean wait penalty, not an estimated quantile:
  semantic TTFT is capped at common T, genuine first-output timeout is assigned T,
  and ordinary failures without semantic output have no TTFT sample. At least five
  fresh observations are required. A 20% near-best band then respects priority.

## Adapter contract

1. ValidatePolicy/NormalizePolicy at administration boundaries. Resolve the profile
   once per request and preserve policy version; construct NewAttemptLedger at
   ingress, including the client's shorter deadline.
2. Preview/Explain are read-only. Select reserves shared SWRR allocation and, when
   necessary, a probe lease. Before dispatch, recheck hard/control eligibility and
   capacity at the authoritative gate. CommitSelection only for an actual admitted
   dispatch; ReleaseSelection compensates a selected but unsent request. Abandoned
   reservations expire and are compensated. Redis failure is an error, never a
   process-local fallback.
3. BeginAttempt is called at actual dispatch, not selection. Every adapter and
   protocol retry uses the same ledger: default total 3, per tier 2, per account 1,
   and one additional dispatch after the first first-output timeout. A request
   cannot move back to a higher-priority tier. MarkSemanticCommit prevents replay.
4. AcquireDispatchBudget is called for actual dispatch; CommitDispatchBudget after
   admission, RefundDispatchBudget only when not sent. Every 10 unique initial
   requests earn one shared retry, burst two. CanRetry is a read-only advisory;
   final acquisition remains atomic. Tokens are not consumed during selection.
5. Observe once per terminal attempt. Set Completed for a successful final result,
   HasSemanticOutput only for real text/reasoning/tool output, and preserve any
   later attributable failure. Exclude client/local-budget/admin cancellations;
   give true first-output timeouts their observed elapsed duration and RetryAfter
   when known. ReleaseProbe when its attempt terminates. Headers, metadata, empty
   deltas and usage-only events are not first semantic output.
6. On a classified shared-limit failure, BlockFailureDomain on the request ledger
   and copy Snapshot.BlockedFailureDomains to SelectionRequest.ExcludedFailureDomains.
   The service persists Retry-After at the provider's real scope; this request-local
   filter never infers a shared domain from a generic 5xx status alone.

## Verification

Run from backend with Go 1.27 (resource-limited command used in this workspace):

    GOMAXPROCS=2 go test -race -p 1 -count=1 ./internal/scheduling
    GOMAXPROCS=2 go test -p 1 -run TestControlledGateway -count=1 ./internal/service

core_test.go uses real Go unit tests and miniredis for shared allocation, concurrent
reservations, compensation/expiry, failure-domain exclusion, actual-dispatch retry
credit and no-local-fallback behavior. postgres/control tests are owned separately.
No test contacts or bills a real model account.


## Adapter boundary and verification

Controlled requests suppress legacy account-health breakers, generic 5xx/overload cooldowns, stream-timeout penalties, empty-response penalties, internal-500 penalties, and same-account generation retries. Credential/authentication failures, model entitlement and confirmed quota gates remain active. Signature/history stripping, budget rectification, compact model fallback and unsupported continuation replay are disabled for the new mode; normal protocol conversion and explicit configured model mapping remain in place.

Admission races (pause/capacity after selection but before dispatch) reselect under a separate 128-selection bound. They never earn/spend retry tokens or actual-attempt ledger entries. Ledger/admission stop errors retain their typed identity through adapters and produce explicit stop reasons without appending a second response after semantic output.

Verified provider limit scope is additive to credential family. Existing Grok OAuth team_id plus canonical model gives a team/model exclusion in the request ledger. Missing metadata never widens the failure domain. Gemini/Antigravity currently parse account/model limits; project_id alone does not distinguish a per-user quota from a project-wide quota, so automatic cross-family Google-project exclusion remains unimplemented pending reliable scope metadata and real upstream fixtures. Pre-existing persisted legacy cooldowns are not bulk-cleared when a policy is enabled; real credential/quota restrictions must not be accidentally erased.

Actual local regression commands (Go 1.27.0, GOMAXPROCS=2):

    go test -tags unit -p 1 -run 'TestControlled|TestOpenAIAPIKeyHealth|TestRateLimitService_HandleUpstreamError_NonOAuth401|TestFailoverState|TestSameAccountRetry' -count=1 ./internal/service ./internal/handler
    go test -race -p 1 -count=1 ./internal/scheduling

Adapter tests use deterministic HTTP stubs to count adapter dispatch calls and verify request preservation. Redis unit tests execute Lua in miniredis; TestDispatchReceiptsRealRedis additionally executes the commit/abort failure chain against two clients of an isolated real Redis server when SCHEDULING_TEST_RECEIPT_REDIS_ADDR is provided. These tests do not certify production Redis clusters, live provider quota scopes or every protocol integration.

Dispatch preparation keeps short-lived compensating Redis receipts. A successful
selection/budget commit can still be undone when a later pre-dispatch gate fails;
repeating commit/abort is idempotent. An initial reservation does not earn retry
credit until commit. Once the caller actually sends, ForgetDispatchReceipts
only removes receipts and never refunds allocation or retry credit. Allocation
and budget calls remain separate Lua operations in their own hash slots. An
expired committed selection receipt is forgotten without adding make-up traffic;
a changed allocation signature is never rolled back into the new pool.

Bedrock EventStream observes decoded Claude JSON, before decoder EOF settlement.
Empty starts/deltas stay buffered until semantic output; thinking and tool
arguments count as semantic output, raw binary bytes do not. A metadata-only or
truncated response cannot become recovery success. This adapter preserves the
legacy path when no controlled observer is attached. Native NDJSON and arbitrary
JSON-array streaming are not claimed as supported protocols by these tests.

Credential refresh remains active: ordinary OAuth cache invalidation and refresh
markers on 401, genuine credential revocation, billing/entitlement failures, and
verified rate limits keep their existing hard protection. Agent Identity task
repair is allowed as a control-plane operation, bounded by the request deadline;
controlled inference does not replay on the same account after repair. The next
logical request may use the repaired task. Antigravity transport failures remain
cross-account failover signals instead of being converted to an early final 502.

Inbound reasoning profile labels are read from the original client body without
rewriting it. Responses/chat use explicit reasoning.effort/reasoning_effort;
Anthropic uses thinking.type, thinking.budget_tokens and output_config.effort;
Gemini uses generationConfig.thinkingConfig.thinkingLevel/thinkingBudget and
the existing snake_case spellings. Explicit recognized levels are lowercase
(none, minimal, low, medium, high, xhigh, max as supported by that protocol).
An explicit integer budget is a separate exact label such as budget:4096,
budget:0 or budget:-1; no budget is converted to low/medium/high. If both an
explicit level and budget are present, the exact label is high|budget:4096
(or the corresponding level/value). thinking.type=disabled uses none. Enabled
or adaptive thinking with no explicit reliable depth, malformed configuration,
and unspecified/unrecognized levels use unknown; no reasoning configuration
uses default. A profile's empty Reasoning remains a wildcard matching every
label. Operators can therefore override high, budget:16384, default or unknown
independently, with a wildcard profile as fallback.

Native Gemini streamGenerateContent and generateContent both use the gemini
transport bucket. Constructor aliases only normalize the explicit known aliases;
responses, chat, messages, gemini, ws and http remain separate. Gemini native
nonempty tools are conservatively replay-unsafe even if an item has no type
field or combines a misleading function/custom type with a server tool field.
This intentionally does not infer provider idempotency for native tools.
