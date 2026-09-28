vi.mock('@/api/admin/schedulingDomains', () => ({ getFailureDomains: vi.fn(), putFailureDomains: vi.fn(), permitFailureRecovery: vi.fn(), getUnknownAttempt: vi.fn(), resolveUnknownAttempt: vi.fn() }))
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
const { getRequestAttempts } = vi.hoisted(() => ({ getRequestAttempts: vi.fn() }))
vi.mock('@/api/admin/scheduling', () => ({ default: { getRequestAttempts } }))
vi.mock('@/utils/format', () => ({ formatDateTime: (value: string) => value }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
import SchedulingRequestAttempts from '../SchedulingRequestAttempts.vue'

const wrappers: ReturnType<typeof mount>[] = []
const record = () => ({ ticket_id: 'ticket-1', request_id: 'request-one', account_id: 42, family_id: 42, state: 'settled', outcome: 'success', cancel_requested: false, usage_pending: false, dispatched_at: '2026-09-27T12:00:00Z', settled_at: '2026-09-27T12:00:01Z', metrics: { priority: 0, reason: 'swrr', first_event_ms: 0, first_semantic_ms: 123, metric_version: 'semantic-v2' } })
function setup() {
  const wrapper = mount(SchedulingRequestAttempts, { props: { initialRequestId: 'request-one' } })
  wrappers.push(wrapper)
  return wrapper
}
beforeEach(() => { vi.resetAllMocks(); getRequestAttempts.mockResolvedValue({ request_id: 'request-one', attempts: [record()] }) })
afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()) })
describe('SchedulingRequestAttempts', () => {
  it('queries only on explicit action and preserves missing metrics instead of zero filling', async () => {
    const wrapper = setup()
    expect(getRequestAttempts).not.toHaveBeenCalled()
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(getRequestAttempts).toHaveBeenCalledWith('request-one', expect.any(AbortSignal))
    expect(wrapper.get('[data-testid="trace-0-first_event_ms"]').text()).toBe('0 ms')
    expect(wrapper.get('[data-testid="trace-0-first_semantic_ms"]').text()).toBe('123 ms')
    expect(wrapper.get('[data-testid="trace-0-first_answer_ms"]').text()).toContain('unobserved')
    expect(wrapper.get('[data-testid="trace-0-overall_first_semantic_ms"]').text()).toContain('unobserved')
    expect(wrapper.get('[data-testid="trace-0-remaining_budget_ms"]').text()).toContain('unobserved')
    expect(wrapper.text()).toContain('#42')
    expect(wrapper.text()).toContain('semantic-v2')
  })
  it('shows an empty registration result without inferring success or failure', async () => {
    getRequestAttempts.mockResolvedValue({ request_id: 'request-one', attempts: [] })
    const wrapper = setup()
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(wrapper.findAll('[data-testid="trace-attempt"]')).toHaveLength(0)
    expect(wrapper.get('[data-testid="trace-empty"]').text()).toContain('traceEmpty')
  })
  it('does not automatically repeat a failed read or keep stale results', async () => {
    const wrapper = setup()
    await wrapper.get('form').trigger('submit'); await flushPromises()
    getRequestAttempts.mockRejectedValueOnce(new Error('unavailable'))
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(getRequestAttempts).toHaveBeenCalledTimes(2)
    expect(wrapper.findAll('[data-testid="trace-attempt"]')).toHaveLength(0)
    expect(wrapper.get('[role="alert"]').exists()).toBe(true)
  })
  it('aborts an outstanding read when the view is closed', async () => {
    let complete!: (value: unknown) => void
    getRequestAttempts.mockReturnValue(new Promise(resolve => { complete = resolve }))
    const wrapper = setup()
    await wrapper.get('form').trigger('submit')
    const signal = getRequestAttempts.mock.calls[0][1] as AbortSignal
    wrapper.unmount()
    expect(signal.aborted).toBe(true)
    complete({ request_id: 'request-one', attempts: [] })
    await flushPromises()
  })
})
