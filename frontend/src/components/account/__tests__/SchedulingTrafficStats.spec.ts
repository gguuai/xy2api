import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
const { getStats } = vi.hoisted(() => ({ getStats: vi.fn() }))
vi.mock('@/api/admin/scheduling', () => ({ default: { getStats } }))
vi.mock('@/utils/format', () => ({ formatDateTime: (value: string) => value }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
import SchedulingTrafficStats from '../SchedulingTrafficStats.vue'

const wrappers: ReturnType<typeof mount>[] = []
const response = () => ({ group_id: 2, model: 'm', since: '2026-09-26T12:00:00Z', until: '2026-09-27T12:00:00Z', ordinary_first_total: 10, accounts: [{ account_id: 1, ordinary_first: 7, ordinary_first_share: 0.7, by_kind: { ordinary_first: 7, retry: 2, probe: 0, pin: 1, owner: 3, fallback: 0 } }] })
function setup() {
  const wrapper = mount(SchedulingTrafficStats, { props: { groupId: 2, model: 'm', candidates: [{ account_id: 1, account_name: 'A', priority: 0, traffic_weight: 7, eligible: true, reason: 'swrr', target_share: 0.7 }] } })
  wrappers.push(wrapper)
  return wrapper
}
beforeEach(() => { vi.resetAllMocks(); getStats.mockResolvedValue(response()) })
afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()) })
describe('SchedulingTrafficStats', () => {
  it('fetches actual counts only on click and displays target separately', async () => {
    const wrapper = setup()
    expect(getStats).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="query-stats"]').trigger('click'); await flushPromises()
    expect(getStats).toHaveBeenCalledWith(2, 'm', expect.any(String), expect.any(AbortSignal))
    expect(wrapper.get('[data-testid="stats-target-1"]').text()).toBe('70.0%')
    expect(wrapper.get('[data-testid="stats-actual-1"]').text()).toBe('70.0%')
    expect(wrapper.findAll('[data-testid="stats-account"]')).toHaveLength(1)
  })
  it('preserves null shares with no denominator and missing preview targets', async () => {
    const data = response()
    data.ordinary_first_total = 0
    getStats.mockResolvedValue({ ...data, accounts: [{ ...data.accounts[0], ordinary_first: 0, ordinary_first_share: null }] })
    const wrapper = setup()
    await wrapper.setProps({ candidates: [] })
    await wrapper.get('[data-testid="query-stats"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="stats-target-1"]').text()).toContain('unobserved')
    expect(wrapper.get('[data-testid="stats-actual-1"]').text()).toContain('unobserved')
  })
  it('discards old scope responses and cancels the old read', async () => {
    let complete!: (value: unknown) => void
    getStats.mockReturnValue(new Promise(resolve => { complete = resolve }))
    const wrapper = setup()
    await wrapper.get('[data-testid="query-stats"]').trigger('click')
    const signal = getStats.mock.calls[0][3] as AbortSignal
    await wrapper.setProps({ groupId: 3 })
    expect(signal.aborted).toBe(true)
    complete(response()); await flushPromises()
    expect(wrapper.findAll('[data-testid="stats-account"]')).toHaveLength(0)
  })
  it('reports an empty observed window without creating traffic rows', async () => {
    getStats.mockResolvedValue({ ...response(), accounts: [], ordinary_first_total: 0 })
    const wrapper = setup()
    await wrapper.get('[data-testid="query-stats"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="stats-empty"]').text()).toContain('statsEmpty')
    expect(wrapper.findAll('[data-testid="stats-account"]')).toHaveLength(0)
  })
})
