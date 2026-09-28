import { beforeEach, describe, expect, it, vi } from 'vitest'
const { get, put, post } = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn(), post: vi.fn() }))
vi.mock('@/api/client', () => ({ default: { get, put, post } }))
import { getFailureDomains, putFailureDomains, getUnknownAttempt, resolveUnknownAttempt, permitFailureRecovery } from '@/api/admin/schedulingDomains'
beforeEach(() => { vi.resetAllMocks() })
describe('failure domain API evidence and CAS contracts', () => {
  it('reads exact account and model without mutation and forwards cancellation', async () => {
    const signal = new AbortController().signal
    get.mockResolvedValue({ data: { eligible: false } })
    await getFailureDomains(3, 'mapped/model', signal)
    expect(get).toHaveBeenCalledWith('/admin/accounts/3/failure-domains', { params: { model: 'mapped/model' }, signal })
    expect(put).not.toHaveBeenCalled()
    expect(post).not.toHaveBeenCalled()
  })
  it('sends domain CAS once and propagates conflict without retry', async () => {
    put.mockRejectedValue({ status: 409 })
    await expect(putFailureDomains({ account_id: 3, version: 7, quota_pool_id: 'org-a', availability_pool_id: '' })).rejects.toEqual({ status: 409 })
    expect(put).toHaveBeenCalledTimes(1)
    expect(put).toHaveBeenCalledWith('/admin/accounts/3/failure-domains', { expected_version: 7, quota_pool_id: 'org-a', availability_pool_id: '' })
  })
  it('encodes ticket identity and forwards verified terminal evidence exactly once', async () => {
    get.mockResolvedValue({ data: { state: 'unknown' } })
    await getUnknownAttempt('ticket/a?b')
    expect(get).toHaveBeenCalledWith('/admin/scheduling/attempts/ticket%2Fa%3Fb/resolution')
    const evidence = { expected_version: 2, source: 'provider_receipt' as const, reference: 'receipt-42', observed_at: '2026-09-28T04:00:00Z', outcome: 'completed' as const, usage_pending: true }
    post.mockRejectedValue({ status: 409 })
    await expect(resolveUnknownAttempt('ticket/a?b', evidence)).rejects.toEqual({ status: 409 })
    expect(post).toHaveBeenCalledTimes(1)
    expect(post).toHaveBeenCalledWith('/admin/scheduling/attempts/ticket%2Fa%3Fb/resolution', evidence)
  })
  it('keeps gate and membership version fences on guarded recovery', async () => {
    const evidence = { gate_key: 'a'.repeat(64), model: 'actual-upstream-model', expected_version: 12, domain_version: 8, source: 'provider_status' as const, reference: 'quota-refilled-42', observed_at: '2026-09-28T04:00:00Z' }
    post.mockResolvedValue({ data: { eligible: true } })
    await permitFailureRecovery(3, evidence)
    expect(post).toHaveBeenCalledWith('/admin/accounts/3/failure-domains/recovery', evidence)
    expect(put).not.toHaveBeenCalled()
  })
})
