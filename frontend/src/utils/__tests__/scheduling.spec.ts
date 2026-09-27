import { describe, expect, it } from 'vitest'
import { createLatencyProfile, createSchedulingPolicy, isSchedulingConflict, validateSchedulingPolicy } from '../scheduling'

describe('controlled scheduling form policy', () => {
  it('starts in legacy mode with explicit retry caps and no latency assumptions', () => {
    const value = createSchedulingPolicy(4, 'my-model')
    expect(value.enabled).toBe(false)
    expect(value.profiles).toEqual([])
    expect(value.pin_fallback).toBe(false)
    expect(value.retry).toMatchObject({ max_attempts: 3, max_per_tier: 2, max_per_account: 1, max_after_timeout: 1, initial_per_token: 10, burst: 2 })
    expect(validateSchedulingPolicy(value)).toBeNull()
    expect(JSON.stringify(value)).not.toMatch(/load_factor|token_speed|tps/i)
  })
  it('requires explicit model identity and a pin target', () => {
    const value = createSchedulingPolicy(0, '')
    expect(validateSchedulingPolicy(value)).toBe('modelRequired')
    value.model = 'specific'; value.mode = 'pin'
    expect(validateSchedulingPolicy(value)).toBe('pinRequired')
    value.pin_account_id = 42
    expect(validateSchedulingPolicy(value)).toBeNull()
  })
  it('preserves inherited priority, permits zero weight, and refuses duplicate IDs', () => {
    const value = createSchedulingPolicy(0, 'model')
    value.accounts = [{ account_id: 1, priority: null, traffic_weight: 0, fill_order: 0 }]
    expect(validateSchedulingPolicy(value)).toBeNull()
    value.accounts.push({ account_id: 1, priority: 4, traffic_weight: 2, fill_order: 1 })
    expect(validateSchedulingPolicy(value)).toBe('duplicateAccount')
  })
  it('rejects fractional weight without rewriting it as load factor', () => {
    const value = createSchedulingPolicy(0, 'model')
    value.accounts = [{ account_id: 1, traffic_weight: 1.5, fill_order: 0 }]
    expect(validateSchedulingPolicy(value)).toBe('invalidWeight')
    expect(value.accounts[0].traffic_weight).toBe(1.5)
  })
  it('allows observe-only profiles and validates exact H/R/T/D/M boundaries', () => {
    const value = createSchedulingPolicy(0, 'model')
    const profile = { ...createLatencyProfile(), name: 'reasoning' }
    value.profiles = [profile]
    expect(validateSchedulingPolicy(value)).toBeNull()
    Object.assign(profile, { health_threshold_ms: 8000, recovery_threshold_ms: 6000, attempt_timeout_ms: 12000, total_budget_ms: 25000, min_attempt_window_ms: 8000 })
    expect(validateSchedulingPolicy(value)).toBeNull()
    profile.total_budget_ms = 11000
    expect(validateSchedulingPolicy(value)).toBe('invalidLatency')
    profile.total_budget_ms = 25000; profile.recovery_threshold_ms = 8000
    expect(validateSchedulingPolicy(value)).toBe('invalidLatency')
  })
  it('requires useful context bounds and distinct profile names', () => {
    const value = createSchedulingPolicy(0, 'model')
    const profile = { ...createLatencyProfile(), name: 'long', context_min_tokens: 1000, context_max_tokens: 500 }
    value.profiles = [profile]
    expect(validateSchedulingPolicy(value)).toBe('invalidContext')
    profile.context_max_tokens = 0
    expect(validateSchedulingPolicy(value)).toBeNull()
    value.profiles.push({ ...profile })
    expect(validateSchedulingPolicy(value)).toBe('profileNameRequired')
  })
  it('refuses unlimited queues and impossible per-tier budgets', () => {
    const value = createSchedulingPolicy(0, 'model')
    value.overflow = 'wait'
    expect(validateSchedulingPolicy(value)).toBe('invalidQueue')
    value.queue_wait_ms = 200
    value.retry.max_per_tier = 4
    expect(validateSchedulingPolicy(value)).toBe('invalidRetry')
  })
  it('recognizes interceptor and axios CAS conflicts without assuming other errors are conflicts', () => {
    expect(isSchedulingConflict({ status: 409 })).toBe(true)
    expect(isSchedulingConflict({ response: { status: 409 } })).toBe(true)
    expect(isSchedulingConflict({ status: 503 })).toBe(false)
    expect(isSchedulingConflict(null)).toBe(false)
  })
})
