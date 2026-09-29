import { describe, expect, it } from 'vitest'
import { cloneGroupPolicy, groupPolicyFingerprint, isGroupSchedulingConflict, validateGroupPolicy } from '../groupScheduling'
import type { GroupSchedulingPolicy } from '@/types/scheduling'
const policy = (): GroupSchedulingPolicy => ({ group_id: 2, version: 7, accounts: [{ account_id: 1, priority: 0, traffic_weight: 1 }, { account_id: 2, priority: 1, traffic_weight: 2 }], first_output_timeout_ms: 120000, total_wait_timeout_ms: 240000, max_attempts: 3 })
describe('group scheduling contracts', () => {
  it('clones account rules without sharing mutable rows', () => { const original = policy(); const copy = cloneGroupPolicy(original); copy.accounts[0].priority = 9; expect(original.accounts[0].priority).toBe(0) })
  it('tracks scope and account changes but ignores display ordering', () => { const original = policy(); const copy = cloneGroupPolicy(original); copy.accounts.reverse(); expect(groupPolicyFingerprint(copy)).toBe(groupPolicyFingerprint(original)); copy.group_id = 3; expect(groupPolicyFingerprint(copy)).not.toBe(groupPolicyFingerprint(original)) })
  it.each([
    ['first_output_timeout_ms', 0, 'invalidWait'], ['first_output_timeout_ms', 3600001, 'invalidWait'],
    ['total_wait_timeout_ms', 119999, 'invalidTotalWait'], ['total_wait_timeout_ms', 7200001, 'invalidTotalWait'],
    ['max_attempts', 0, 'invalidAttempts'], ['max_attempts', 2.5, 'invalidAttempts'], ['max_attempts', 11, 'invalidAttempts']
  ] as const)('rejects %s=%s', (key, value, expected) => { const draft = policy(); draft[key] = value; expect(validateGroupPolicy(draft)).toBe(expected) })
  it('validates priority, weights and duplicate account IDs', () => { const draft = policy(); draft.accounts[0].traffic_weight = 0; expect(validateGroupPolicy(draft)).toBeNull(); draft.accounts[0].traffic_weight = 0.5; expect(validateGroupPolicy(draft)).toBe('invalidWeight'); draft.accounts[0].traffic_weight = 1; draft.accounts[0].priority = 2147483648; expect(validateGroupPolicy(draft)).toBe('invalidPriority'); draft.accounts[0].priority = 0; draft.accounts.push({ ...draft.accounts[0] }); expect(validateGroupPolicy(draft)).toBe('invalidAccounts') })
  it('accepts the explicitly defined ungrouped scope and recognizes either API error shape', () => { const draft = policy(); draft.group_id = 0; expect(validateGroupPolicy(draft)).toBeNull(); expect(isGroupSchedulingConflict({ status: 409 })).toBe(true); expect(isGroupSchedulingConflict({ response: { status: 409 } })).toBe(true); expect(isGroupSchedulingConflict({ status: 400 })).toBe(false) })
})
