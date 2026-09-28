vi.mock('@/api/admin/schedulingDomains', () => ({ getFailureDomains: vi.fn(), putFailureDomains: vi.fn(), permitFailureRecovery: vi.fn(), getUnknownAttempt: vi.fn(), resolveUnknownAttempt: vi.fn() }))
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
const { getControl, setControl } = vi.hoisted(() => ({ getControl: vi.fn(), setControl: vi.fn() }))
vi.mock('@/api/admin/scheduling', () => ({ default: { getControl, setControl } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/utils/format', () => ({ formatDateTime: (value: string) => value }))
import AccountSchedulingControlDialog from '../AccountSchedulingControlDialog.vue'
const control = (id = 4) => ({ account_id: id, scope: 'logical_account', state: 'RUNNING', epoch: 9, active_attempts: 2, unknown_attempts: 0, pending_settlements: 1, allowed_sessions: 0, session_turn_limit: 0, updated_at: '' })
const wrappers: ReturnType<typeof mount>[] = []
async function setup() {
  const wrapper = mount(AccountSchedulingControlDialog, { props: { show: true, account: { id: 4, name: 'primary' } }, global: { stubs: { BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' } } } })
  wrappers.push(wrapper); await flushPromises(); return wrapper
}
beforeEach(() => { vi.useFakeTimers(); vi.resetAllMocks(); getControl.mockResolvedValue(control()); setControl.mockResolvedValue({ ...control(), state: 'DRAINING', epoch: 10 }) })
afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()); vi.useRealTimers() })
describe('account scheduling control', () => {
  it('defaults to logical-account graceful pause and sends the observed epoch', async () => {
    const wrapper = await setup()
    expect(getControl).toHaveBeenCalledWith(4, 'logical_account', expect.any(AbortSignal))
    expect(setControl).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="apply-control"]').trigger('click'); await flushPromises()
    expect(setControl).toHaveBeenCalledWith(4, { action: 'pause', scope: 'logical_account', expected_epoch: 9 })
    expect(wrapper.get('[data-testid="control-state"]').text()).toContain('DRAINING')
    expect(wrapper.emitted('updated')?.[0][0]).toMatchObject({ state: 'DRAINING', active_attempts: 2 })
  })
  it('requires acknowledging interruptions before force stop', async () => {
    const wrapper = await setup()
    await wrapper.get('[data-testid="control-action"]').setValue('force_stop')
    expect(wrapper.get('[data-testid="apply-control"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="force-confirmed"]').setValue(true)
    await wrapper.get('[data-testid="apply-control"]').trigger('click'); await flushPromises()
    expect(setControl).toHaveBeenCalledWith(4, expect.objectContaining({ action: 'force_stop', expected_epoch: 9 }))
  })
  it('requires positive bounded session duration and turns', async () => {
    const wrapper = await setup()
    await wrapper.get('[data-testid="control-action"]').setValue('session_drain')
    await wrapper.get('[data-testid="session-seconds"]').setValue('0')
    expect(wrapper.get('[data-testid="apply-control"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="session-seconds"]').setValue('120')
    await wrapper.get('[data-testid="session-turns"]').setValue('2')
    await wrapper.get('[data-testid="apply-control"]').trigger('click'); await flushPromises()
    expect(setControl).toHaveBeenCalledWith(4, { action: 'session_drain', scope: 'logical_account', expected_epoch: 9, session_duration_seconds: 120, session_max_turns: 2 })
  })
  it('displays CAS conflict and requires refreshing rather than replaying the action', async () => {
    const wrapper = await setup(); setControl.mockRejectedValueOnce({ status: 409 })
    await wrapper.get('[data-testid="apply-control"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('controlConflict')
    expect(wrapper.get('[data-testid="apply-control"]').attributes('disabled')).toBeDefined()
    await vi.advanceTimersByTimeAsync(10000); expect(setControl).toHaveBeenCalledTimes(1)
  })
  it('never applies an old account response after switching accounts', async () => {
    let resolveOld!: (value: unknown) => void
    getControl.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve }))
    const wrapper = await setup()
    getControl.mockResolvedValueOnce({ ...control(5), state: 'PAUSED', epoch: 12 })
    await wrapper.setProps({ account: { id: 5, name: 'secondary' } }); await flushPromises()
    resolveOld(control()); await flushPromises()
    expect(wrapper.get('[data-testid="control-state"]').text()).toContain('PAUSED')
    await wrapper.get('[data-testid="control-action"]').setValue('resume')
    await wrapper.get('[data-testid="apply-control"]').trigger('click'); await flushPromises()
    expect(setControl).toHaveBeenCalledWith(5, expect.objectContaining({ action: 'resume', expected_epoch: 12 }))
  })
  it('stops polling when the dialog closes', async () => {
    const wrapper = await setup(); expect(getControl).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(5000); await flushPromises(); expect(getControl).toHaveBeenCalledTimes(2)
    await wrapper.setProps({ show: false }); await vi.advanceTimersByTimeAsync(10000)
    expect(getControl).toHaveBeenCalledTimes(2)
  })
  it('disables mutations if control state is unreadable', async () => {
    getControl.mockRejectedValueOnce(new Error('offline'))
    const wrapper = await setup()
    expect(wrapper.get('[role="alert"]').text()).toContain('offline')
    expect(wrapper.get('[data-testid="apply-control"]').attributes('disabled')).toBeDefined()
  })
  it('warns after five minutes of draining without issuing a force stop', async () => {
    getControl.mockResolvedValue({ ...control(), state: 'DRAINING', updated_at: new Date(Date.now() - 301000).toISOString() })
    const wrapper = await setup()
    expect(wrapper.get('[data-testid="drain-warning"]').text()).toContain('drainOverdue')
    await vi.advanceTimersByTimeAsync(5000); await flushPromises()
    expect(setControl).not.toHaveBeenCalled()
    expect(wrapper.get('[data-testid="control-action"]').element.value).toBe('pause')
  })
})
