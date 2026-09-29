import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
const api = vi.hoisted(() => ({ getSchedulingMode: vi.fn(), setSchedulingMode: vi.fn() }))
vi.mock('@/api/admin/schedulingMode', () => api)
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
import SchedulingModeSettings from '../SchedulingModeSettings.vue'
import { useSchedulingModeStore } from '@/stores/schedulingMode'
const wrappers: ReturnType<typeof mount>[] = []
async function setup() { const wrapper = mount(SchedulingModeSettings, { global: { stubs: { RouterLink: { template: '<a><slot /></a>' } } } }); wrappers.push(wrapper); await flushPromises(); return wrapper }
beforeEach(() => { vi.resetAllMocks(); api.getSchedulingMode.mockResolvedValue({ mode: 'sub2api', version: 4 }); api.setSchedulingMode.mockResolvedValue({ mode: 'controlled', version: 5 }) })
afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()) })
describe('system settings scheduling mode', () => {
  it('reads without switching; applying a selection uses the observed version', async () => {
    const wrapper = await setup()
    expect(wrapper.get('[data-testid="mode-sub2api"]').element).toHaveProperty('checked', true)
    expect(api.setSchedulingMode).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="mode-controlled"]').setValue(true)
    await wrapper.get('[data-testid="save-scheduling-mode"]').trigger('click'); await flushPromises()
    expect(api.setSchedulingMode).toHaveBeenCalledWith('controlled', 4)
    expect(wrapper.get('[role="status"]').text()).toContain('modeSettings.saved')
    expect(useSchedulingModeStore().isControlled).toBe(true)
  })
  it('preserves the choice after conflict and uses the refreshed version only on explicit apply', async () => {
    const wrapper = await setup()
    await wrapper.get('[data-testid="mode-controlled"]').setValue(true)
    api.setSchedulingMode.mockRejectedValueOnce({ status: 409 })
    api.getSchedulingMode.mockResolvedValue({ mode: 'sub2api', version: 6 })
    await wrapper.get('[data-testid="save-scheduling-mode"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="mode-controlled"]').element).toHaveProperty('checked', true)
    expect(wrapper.get('[role="alert"]').text()).toContain('modeSettings.conflict')
    expect(api.setSchedulingMode).toHaveBeenCalledTimes(1)
    await wrapper.get('[data-testid="save-scheduling-mode"]').trigger('click'); await flushPromises()
    expect(api.setSchedulingMode).toHaveBeenLastCalledWith('controlled', 6)
  })
  it('offers a retry without showing an unconfirmed mode on initial failure', async () => {
    api.getSchedulingMode.mockRejectedValueOnce(new Error('offline'))
    const wrapper = await setup()
    expect(wrapper.find('input[type="radio"]').exists()).toBe(false)
    expect(wrapper.get('[role="alert"]').text()).toContain('loadFailed')
    await wrapper.get('button').trigger('click'); await flushPromises()
    expect(wrapper.find('[data-testid="mode-sub2api"]').exists()).toBe(true)
  })
  it('retains the selection on a failed save and never mutates unrelated settings', async () => {
    const wrapper = await setup(); api.setSchedulingMode.mockRejectedValueOnce(new Error('offline'))
    await wrapper.get('[data-testid="mode-controlled"]').setValue(true)
    await wrapper.get('[data-testid="save-scheduling-mode"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="mode-controlled"]').element).toHaveProperty('checked', true)
    expect(useSchedulingModeStore().document?.mode).toBe('sub2api')
    expect(wrapper.get('[role="alert"]').text()).toContain('offline')
  })
})
