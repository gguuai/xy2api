import type { ModelLatencyProfile, SchedulingPolicy } from '@/types/scheduling'

export const latencyKeys = ['health_threshold_ms', 'recovery_threshold_ms', 'attempt_timeout_ms', 'total_budget_ms', 'min_attempt_window_ms'] as const
export type LatencyKey = typeof latencyKeys[number]
export function createSchedulingPolicy(groupID: number, model: string): SchedulingPolicy {
  return {
    group_id: groupID, model, version: 0, enabled: false, mode: 'swrr', accounts: [], profiles: [],
    overflow: 'immediate', queue_wait_ms: 0, all_degraded: 'bounded_best_effort', pin_fallback: false,
    retry: { max_attempts: 3, max_per_tier: 2, max_per_account: 1, max_after_timeout: 1,
      initial_per_token: 10, burst: 2, switch_margin_ms: 200, reserve_fallback: true, cross_tier: true,
      mode: 'bounded_same_tier_first' }
  }
}
export function createLatencyProfile(): ModelLatencyProfile {
  return { name: '', reasoning: '', transport: '', context_min_tokens: 0, context_max_tokens: 0,
    health_threshold_ms: 0, recovery_threshold_ms: 0, attempt_timeout_ms: 0, total_budget_ms: 0, min_attempt_window_ms: 0 }
}
export function validateSchedulingPolicy(policy: SchedulingPolicy): string | null {
  if (!policy.model.trim()) return 'modelRequired'
  if (policy.mode === 'pin' && !policy.pin_account_id) return 'pinRequired'
  if (!Number.isSafeInteger(policy.queue_wait_ms) || policy.queue_wait_ms < 0 || policy.queue_wait_ms > 60000 || (policy.overflow === 'wait' && policy.queue_wait_ms <= 0)) return 'invalidQueue'
  const ids = new Set<number>()
  for (const account of policy.accounts) {
    if (!Number.isInteger(account.account_id) || account.account_id < 1 || ids.has(account.account_id)) return 'duplicateAccount'
    ids.add(account.account_id)
    if (!Number.isSafeInteger(account.traffic_weight) || account.traffic_weight < 0 || account.traffic_weight > 1000000) return 'invalidWeight'
    if (account.priority != null && !Number.isSafeInteger(account.priority)) return 'invalidPriority'
    if (!Number.isSafeInteger(account.fill_order)) return 'invalidOrder'
  }
  const names = new Set<string>()
  for (const profile of policy.profiles) {
    if (!profile.name.trim() || names.has(profile.name.trim())) return 'profileNameRequired'
    names.add(profile.name.trim())
    if (latencyKeys.some(key => !Number.isSafeInteger(profile[key]) || profile[key] < 0)) return 'invalidLatency'
    const [h, r, t, d, m] = latencyKeys.map(key => profile[key])
    if ([h,r,t,d,m].some(Boolean) && !(0 < r && r < h && h < t && t <= d && d <= 1800000 && 0 < m && m <= t)) return 'invalidLatency'
    if (!Number.isSafeInteger(profile.context_min_tokens ?? 0) || !Number.isSafeInteger(profile.context_max_tokens ?? 0) ||
      (profile.context_min_tokens ?? 0) < 0 || (profile.context_max_tokens ?? 0) < 0 ||
      (profile.context_max_tokens && profile.context_max_tokens <= (profile.context_min_tokens ?? 0))) return 'invalidContext'
  }
  if (overlappingProfiles(policy.profiles).length) return 'ambiguousProfiles'
  const retry = policy.retry
  for (const key of ['max_attempts', 'max_per_tier', 'max_per_account', 'initial_per_token', 'burst'] as const) {
    if (!Number.isSafeInteger(retry[key]) || retry[key] < 1) return 'invalidRetry'
  }
  if (!Number.isSafeInteger(retry.max_after_timeout) || retry.max_after_timeout < 0 || retry.max_after_timeout > 1 || !Number.isSafeInteger(retry.switch_margin_ms) || retry.switch_margin_ms < 0 || retry.max_attempts > 10 || retry.burst > 100) return 'invalidRetry'
  if (retry.max_per_tier > retry.max_attempts || retry.max_per_account > retry.max_attempts) return 'invalidRetry'
  return null
}
export function isSchedulingConflict(error: unknown): boolean {
  if (!error || typeof error !== 'object') return false
  const value = error as { status?: number; response?: { status?: number } }
  return value.status === 409 || value.response?.status === 409
}

// Match backend selector specificity and half-open context ranges exactly.
export function overlappingProfiles(profiles: ModelLatencyProfile[]): string[][] {
  const transport = (raw = '') => {
    const value = raw.trim().toLowerCase()
    return ({ anthropic: 'messages', chat_completions: 'chat', response: 'responses', websocket: 'ws' } as Record<string, string>)[value] ?? value
  }
  const specificity = (p: ModelLatencyProfile) => (p.reasoning ? 4 : 0) + (transport(p.transport) ? 2 : 0) + ((p.context_min_tokens ?? 0) > 0 || (p.context_max_tokens ?? 0) > 0 ? 1 : 0)
  const reasoning = (raw = '') => raw.trim().toLowerCase() || 'default'
  const overlaps: string[][] = []
  profiles.forEach((a, i) => profiles.slice(i + 1).forEach(b => {
    if (specificity(a) !== specificity(b) || (a.reasoning && reasoning(a.reasoning) !== reasoning(b.reasoning)) || transport(a.transport) !== transport(b.transport)) return
    if ((a.context_max_tokens && a.context_max_tokens <= (b.context_min_tokens ?? 0)) || (b.context_max_tokens && b.context_max_tokens <= (a.context_min_tokens ?? 0))) return
    overlaps.push([a.name, b.name])
  }))
  return overlaps
}
