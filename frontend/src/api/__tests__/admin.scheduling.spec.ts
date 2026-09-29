import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { GroupSchedulingPolicy } from '@/types/scheduling'
const { get, put } = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get, put } }))
import scheduling from '@/api/admin/scheduling'
beforeEach(() => { vi.resetAllMocks() })
describe('group scheduling API', () => {
  it('exposes only group policy operations, without diagnostic or pause APIs', () => { expect(Object.keys(scheduling).sort()).toEqual(['getGroupPolicy', 'saveGroupPolicy']) })
  it('loads exact group scope with cancellation and no model parameter', async () => { const signal = new AbortController().signal; get.mockResolvedValue({ data: { version: 7 } }); await scheduling.getGroupPolicy(2, signal); expect(get).toHaveBeenCalledWith('/admin/scheduling/groups/2', { signal }) })
  it('writes an explicit policy and CAS version once, never automatically retries conflicts', async () => { const policy: GroupSchedulingPolicy = { group_id: 2, version: 7, accounts: [{ account_id: 1, priority: 0, traffic_weight: 1 }], first_output_timeout_ms: 120000, total_wait_timeout_ms: 240000, max_attempts: 3 }; put.mockRejectedValue({ status: 409 }); await expect(scheduling.saveGroupPolicy(policy, 7)).rejects.toEqual({ status: 409 }); expect(put).toHaveBeenCalledTimes(1); expect(put).toHaveBeenCalledWith('/admin/scheduling/groups/2', { expected_version: 7, policy }) })
})
