import type { GroupSchedulingPolicy } from '@/types/scheduling'

export function cloneGroupPolicy(policy: GroupSchedulingPolicy): GroupSchedulingPolicy {
  return { ...policy, accounts: policy.accounts.map(account => ({ ...account })) }
}

/** Stable comparison ignores display order, never group identity or configured values. */
export function groupPolicyFingerprint(policy: GroupSchedulingPolicy): string {
  return JSON.stringify({
    group_id: policy.group_id,
    first_output_timeout_ms: policy.first_output_timeout_ms,
    total_wait_timeout_ms: policy.total_wait_timeout_ms,
    max_attempts: policy.max_attempts,
    accounts: [...policy.accounts].sort((a, b) => a.account_id - b.account_id).map(({ account_id, priority, traffic_weight }) => ({ account_id, priority, traffic_weight }))
  })
}

export function validateGroupPolicy(policy: GroupSchedulingPolicy): string | null {
  if (!Number.isSafeInteger(policy.group_id) || policy.group_id < 0) return 'selectGroup'
  if (!Number.isSafeInteger(policy.first_output_timeout_ms) || policy.first_output_timeout_ms < 1000 || policy.first_output_timeout_ms > 3600000) return 'invalidWait'
  if (!Number.isSafeInteger(policy.total_wait_timeout_ms) || policy.total_wait_timeout_ms < policy.first_output_timeout_ms || policy.total_wait_timeout_ms > 7200000) return 'invalidTotalWait'
  if (!Number.isSafeInteger(policy.max_attempts) || policy.max_attempts < 1 || policy.max_attempts > 10) return 'invalidAttempts'
  const ids = new Set<number>()
  for (const account of policy.accounts) {
    if (!Number.isSafeInteger(account.account_id) || account.account_id <= 0 || ids.has(account.account_id)) return 'invalidAccounts'
    ids.add(account.account_id)
    if (!Number.isSafeInteger(account.priority) || account.priority < -2147483648 || account.priority > 2147483647) return 'invalidPriority'
    if (!Number.isSafeInteger(account.traffic_weight) || account.traffic_weight < 0 || account.traffic_weight > 1000000) return 'invalidWeight'
  }
  return null
}

export function isGroupSchedulingConflict(error: unknown): boolean {
  const value = error as { status?: number; response?: { status?: number } } | null
  return value?.status === 409 || value?.response?.status === 409
}
