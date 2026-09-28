import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createSchedulingPolicy } from '@/utils/scheduling'
const { get, put, post, remove } = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn(), post: vi.fn(), remove: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get, put, post, delete: remove } }))
import scheduling from '@/api/admin/scheduling'

beforeEach(() => { vi.resetAllMocks() })
describe('admin scheduling API contracts', () => {
  it('loads exact group/model scope and forwards cancellation', async () => {
    const signal = new AbortController().signal
    get.mockResolvedValue({ data: { policy: null, group_id: 2, model: 'custom', version: 0 } })
    await scheduling.getPolicy(2, 'custom', signal)
    expect(get).toHaveBeenCalledWith('/admin/scheduling/policies', { params: { group_id: 2, model: 'custom' }, signal })
  })
  it('writes expected_version separately and does not replay a conflict', async () => {
    const policy = createSchedulingPolicy(2, 'custom')
    put.mockRejectedValue({ status: 409 })
    await expect(scheduling.savePolicy(policy, 8)).rejects.toEqual({ status: 409 })
    expect(put).toHaveBeenCalledTimes(1)
    expect(put).toHaveBeenCalledWith('/admin/scheduling/policies', { group_id: 2, model: 'custom', expected_version: 8, policy })
  })
  it('posts explain without mutating policy or fabricating traffic values', async () => {
    const request = { group_id: 0, model: 'custom', protocol: 'responses', reasoning_effort: 'high' }
    post.mockResolvedValue({ data: { readonly: true, candidates: [] } })
    await scheduling.explain(request)
    expect(post).toHaveBeenCalledWith('/admin/scheduling/explain', request, { signal: undefined })
    expect(put).not.toHaveBeenCalled()
  })
  it('reads logical account scope by default and sends epoch on control operations', async () => {
    get.mockResolvedValue({ data: { epoch: 9 } }); post.mockResolvedValue({ data: { epoch: 10 } })
    await scheduling.getControl(7)
    expect(get).toHaveBeenCalledWith('/admin/accounts/7/scheduling-control', { params: { scope: 'logical_account' }, signal: undefined })
    const command = { action: 'pause' as const, scope: 'logical_account' as const, expected_epoch: 9 }
    await scheduling.setControl(7, command)
    expect(post).toHaveBeenCalledWith('/admin/accounts/7/scheduling-control', command)
  })
  it('reads actual request tickets with an encoded ID and never creates attempts', async () => {
    const signal = new AbortController().signal
    get.mockResolvedValue({ data: { request_id: 'request/one', attempts: [] } })
    await scheduling.getRequestAttempts('request/one', signal)
    expect(get).toHaveBeenCalledWith('/admin/scheduling/requests/request%2Fone/attempts', { signal })
    expect(post).not.toHaveBeenCalled()
    expect(put).not.toHaveBeenCalled()
  })
  it('reads traffic in the exact group/model and explicit time window', async () => {
    get.mockResolvedValue({ data: { accounts: [] } })
    await scheduling.getStats(2, 'custom', '2026-09-26T12:00:00Z')
    expect(get).toHaveBeenCalledWith('/admin/scheduling/stats', { params: { group_id: 2, model: 'custom', since: '2026-09-26T12:00:00Z' }, signal: undefined })
    expect(post).not.toHaveBeenCalled()
    expect(put).not.toHaveBeenCalled()
  })
})

it('restores inheritance with the current CAS version and propagates a conflict once', async () => {
  remove.mockRejectedValue({ status: 409 })
  await expect(scheduling.restoreInheritance(2, 'custom', 8)).rejects.toEqual({ status: 409 })
  expect(remove).toHaveBeenCalledTimes(1)
  expect(remove).toHaveBeenCalledWith('/admin/scheduling/policies', { data: { group_id: 2, model: 'custom', expected_version: 8 } })
})
it('sends explicit profile resets through the existing policy CAS mutation', async () => {
  put.mockResolvedValue({ data: { version: 9 } })
  const policy = createSchedulingPolicy(2, 'custom')
  await scheduling.savePolicy(policy, 8, ['high'])
  expect(put).toHaveBeenCalledWith('/admin/scheduling/policies', { group_id: 2, model: 'custom', expected_version: 8, policy, reset_health_profiles: ['high'] })
})
