/** Duration wire fields are milliseconds unless explicitly named otherwise. */
export interface AccountSchedulingRule { account_id: number; priority?: number | null; traffic_weight: number; fill_order: number }
export interface ModelLatencyProfile {
  name: string; health_revision?: number; reasoning?: string; transport?: string; context_min_tokens?: number; context_max_tokens?: number
  health_threshold_ms: number; recovery_threshold_ms: number; attempt_timeout_ms: number
  total_budget_ms: number; min_attempt_window_ms: number
}
export interface SchedulingRetryPolicy {
  max_attempts: number; max_per_tier: number; max_per_account: number; max_after_timeout: number
  initial_per_token: number; burst: number; switch_margin_ms: number; reserve_fallback: boolean; cross_tier: boolean
  mode: 'bounded_same_tier_first' | 'exhaust_same_tier'
}
export interface SchedulingPolicy {
  group_id: number; model: string; version: number; enabled: boolean
  mode: 'swrr' | 'pin' | 'fill_first'; accounts: AccountSchedulingRule[]; profiles: ModelLatencyProfile[]
  retry: SchedulingRetryPolicy; queue_wait_ms: number; overflow: 'immediate' | 'wait'
  all_degraded: 'bounded_best_effort' | 'strict_priority' | 'fail_fast'
  pin_account_id?: number; pin_fallback: boolean
}
export interface SchedulingProfileDiagnostic { code: string; profiles: string[] }
export interface SchedulingPolicyDocument { diagnostics?: SchedulingProfileDiagnostic[]; policy: SchedulingPolicy | null; version: number; group_id: number; model: string }
export type SchedulingControlScope = 'logical_account' | 'credential_family'
export type SchedulingControlState = 'RUNNING' | 'DRAINING' | 'PAUSED' | 'DRAIN_UNCERTAIN'
export interface AccountSchedulingControl {
  account_id: number; scope: SchedulingControlScope; state: SchedulingControlState; epoch: number
  active_attempts: number; unknown_attempts: number; allowed_sessions: number; pending_settlements: number
  updated_at: string; session_deadline?: string | null; session_turn_limit: number; notification_warning?: string
}
export interface SchedulingControlCommand {
  action: 'pause' | 'resume' | 'force_stop' | 'session_drain'; scope: SchedulingControlScope; expected_epoch: number
  session_duration_seconds?: number; session_max_turns?: number
}
export interface SchedulingExplainRequest {
  group_id: number; model: string; protocol: string; reasoning_effort: string
  context_tokens?: number; pin_account_id?: number
}
export interface SchedulingExplainCandidate {
  account_id: number; account_name: string; priority: number; traffic_weight: number
  eligible: boolean; reason: string; health?: string; control_state?: string; target_share?: number
  recovery_stage?: number | null; good_streak?: number; cooldown_until?: string | null; effective_share?: number
  family_control_state?: string; current_concurrency?: number; concurrency_limit?: number
}
export interface SchedulingExplanation {
  profile?: ModelLatencyProfile; profile_source?: string; context_bucket?: string; context_tokens_known?: boolean
  context_source?: string; reasoning_effort?: string; profile_diagnostics?: SchedulingProfileDiagnostic[]
  policy_version: number; mode: string; selected_account_id?: number; reason: string
  candidates: SchedulingExplainCandidate[]; readonly: true; snapshot_at?: string
}
export interface SchedulingAttemptMetrics {
  priority?: number; reason?: string; metric_version?: string; policy_version?: number
  first_event_ms?: number; first_semantic_ms?: number; first_answer_ms?: number
  overall_first_semantic_ms?: number; remaining_budget_ms?: number; stop_reason?: string
}
export interface SchedulingAttemptRecord {
  ticket_id: string; request_id: string; account_id: number; family_id: number
  state: string; outcome: string; cancel_requested: boolean; usage_pending: boolean
  dispatched_at: string; settled_at: string | null; metrics: SchedulingAttemptMetrics
}
export interface SchedulingRequestAttempts { request_id: string; attempts: SchedulingAttemptRecord[] }
export type SchedulingDispatchKind = 'ordinary_first' | 'retry' | 'probe' | 'pin' | 'owner' | 'fallback'
export interface SchedulingTrafficAccount {
  account_id: number; ordinary_first: number; ordinary_first_share: number | null
  by_kind: Partial<Record<SchedulingDispatchKind, number>>
}
export interface SchedulingTrafficStats {
  group_id: number; model: string; since: string; until: string
  ordinary_first_total: number; accounts: SchedulingTrafficAccount[]
}
