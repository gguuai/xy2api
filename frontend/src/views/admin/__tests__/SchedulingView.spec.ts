import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createSchedulingPolicy } from '@/utils/scheduling'
import englishScheduling from '@/i18n/locales/en/admin/scheduling'
import chineseScheduling from '@/i18n/locales/zh/admin/scheduling'
const { getPolicy, savePolicy, explain, list, getAllIncludingInactive } = vi.hoisted(() => ({ getPolicy: vi.fn(), savePolicy: vi.fn(), explain: vi.fn(), list: vi.fn(), getAllIncludingInactive: vi.fn() }))
vi.mock('@/api/admin/scheduling', () => ({ default: { getPolicy, savePolicy, explain } }))
vi.mock('@/api/admin/accounts', () => ({ default: { list } }))
vi.mock('@/api/admin/groups', () => ({ default: { getAllIncludingInactive } }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
vi.mock('@/components/account/AccountSchedulingControlDialog.vue', () => ({ default: { template: '<div />' } }))
vi.mock('vue-router', () => ({ useRoute: () => ({ query: {} }) }))
vi.mock('@/utils/format', () => ({ formatDateTime: (value: string) => value }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
import SchedulingView from '../SchedulingView.vue'

const wrappers: ReturnType<typeof mount>[] = []
const savedDocument = () => ({ group_id: 0, model: 'custom', version: 7, policy: { ...createSchedulingPolicy(0, 'custom'), version: 7 } })
async function setup() {
  const wrapper = mount(SchedulingView, { global: { stubs: { AppLayout: { template: '<main><slot /></main>' }, RouterLink: { template: '<a><slot /></a>' }, AccountSchedulingControlDialog: true } } })
  wrappers.push(wrapper)
  await flushPromises()
  await wrapper.get('[data-testid="scope-model"]').setValue('custom')
  await wrapper.get('[data-testid="load-policy"]').trigger('click')
  await flushPromises()
  return wrapper
}
beforeEach(() => {
  vi.resetAllMocks()
  getAllIncludingInactive.mockResolvedValue([{ id: 2, name: 'group' }])
  list.mockResolvedValue({ items: [{ id: 1, name: 'primary', priority: 5, concurrency: 10, group_ids: [2] }, { id: 2, name: 'secondary', priority: 10, concurrency: 8, group_ids: [2] }], total: 2 })
  getPolicy.mockResolvedValue(savedDocument())
  savePolicy.mockImplementation(async (policy: unknown) => ({ policy, version: 8, group_id: 0, model: 'custom' }))
  explain.mockResolvedValue({ policy_version: 7, mode: 'legacy', reason: 'legacy', candidates: [], readonly: true })
})
afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()) })
describe('SchedulingView', () => {
  it('loads legacy and observe-only without performing a mutation', async () => {
    const wrapper = await setup()
    expect(getPolicy).toHaveBeenCalledWith(0, 'custom', expect.any(AbortSignal))
    expect(wrapper.get('[data-testid="policy-enabled"]').element.checked).toBe(false)
    expect(wrapper.get('[data-testid="observe-only"]').text()).toContain('observeOnly')
    expect(savePolicy).not.toHaveBeenCalled()
    expect(explain).not.toHaveBeenCalled()
  })
  it('creates account overrides with inherited priority and explicit equal weight', async () => {
    const wrapper = await setup()
    await wrapper.get('[data-testid="add-account-select"]').setValue(1)
    await wrapper.get('[data-testid="add-account"]').trigger('click')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(savePolicy).toHaveBeenCalledWith(expect.objectContaining({ accounts: [{ account_id: 1, priority: null, traffic_weight: 1, fill_order: 0 }] }), 7)
  })
  it('converts seconds to integer milliseconds and uses H-derived defaults only on explicit action', async () => {
    const wrapper = await setup()
    await wrapper.get('[data-testid="add-profile"]').trigger('click')
    const fields = { health_threshold_ms: '8', recovery_threshold_ms: '6', attempt_timeout_ms: '12.125', total_budget_ms: '25', min_attempt_window_ms: '8' }
    for (const [key, value] of Object.entries(fields)) await wrapper.get('[data-testid="profile-0-' + key + '"]').setValue(value)
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(savePolicy.mock.calls[0][0].profiles[0]).toMatchObject({ health_threshold_ms: 8000, recovery_threshold_ms: 6000, attempt_timeout_ms: 12125, total_budget_ms: 25000, min_attempt_window_ms: 8000 })
  })
  it('blocks invalid latency relationships instead of submitting them', async () => {
    const wrapper = await setup()
    await wrapper.get('[data-testid="add-profile"]').trigger('click')
    await wrapper.get('[data-testid="profile-0-health_threshold_ms"]').setValue('8')
    await wrapper.get('form').trigger('submit')
    expect(savePolicy).not.toHaveBeenCalled()
    expect(wrapper.get('[role="alert"]').text()).toContain('invalidLatency')
  })
  it('retains the local draft after CAS conflict and prevents an automatic overwrite', async () => {
    const wrapper = await setup()
    await wrapper.get('[data-testid="policy-enabled"]').setValue(true)
    savePolicy.mockRejectedValueOnce({ status: 409 })
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(wrapper.get('[data-testid="policy-enabled"]').element.checked).toBe(true)
    expect(wrapper.get('[data-testid="save-policy"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[role="alert"]').text()).toContain('policyConflict')
    await wrapper.get('form').trigger('submit'); expect(savePolicy).toHaveBeenCalledTimes(1)
  })
  it('explain sends saved scope only and never saves unsaved form changes', async () => {
    const wrapper = await setup()
    await wrapper.get('[data-testid="policy-enabled"]').setValue(true)
    await wrapper.get('[data-testid="explain"]').trigger('click'); await flushPromises()
    expect(explain).toHaveBeenCalledWith({ group_id: 0, model: 'custom', protocol: 'responses', reasoning_effort: '' }, expect.any(AbortSignal))
    expect(savePolicy).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('noActualMetrics')
  })
  it('requires reloading after a scope change and does not save to a different model', async () => {
    const wrapper = await setup()
    await wrapper.get('[data-testid="scope-model"]').setValue('other')
    expect(wrapper.get('[data-testid="save-policy"]').attributes('disabled')).toBeDefined()
    await wrapper.get('form').trigger('submit'); expect(savePolicy).not.toHaveBeenCalled()
  })
  it('keeps a new model in legacy mode when no persisted policy exists', async () => {
    getPolicy.mockResolvedValue({ policy: null, version: 0, group_id: 0, model: 'custom' })
    const wrapper = await setup()
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(savePolicy).toHaveBeenCalledWith(expect.objectContaining({ enabled: false, profiles: [], mode: 'swrr' }), 0)
  })
  it('shows the server recovery stage relative to normal share', async () => {
    explain.mockResolvedValue({ policy_version: 7, mode: 'swrr', reason: 'swrr', readonly: true, candidates: [{ account_id: 1, account_name: 'primary', priority: 0, traffic_weight: 1, eligible: true, reason: 'recovery', health: 'recovering', control_state: 'RUNNING', target_share: 0.01, effective_share: 0.001, recovery_stage: 0, good_streak: 2 }] })
    const wrapper = await setup()
    await wrapper.get('[data-testid="explain"]').trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('10%')
    expect(wrapper.text()).toContain('0.10%')
    expect(wrapper.text()).toContain('recoveryGood 2')
    expect(savePolicy).not.toHaveBeenCalled()
  })
  it('shows and copies an inherited model policy without claiming legacy is active', async () => {
    const inherited = { ...createSchedulingPolicy(0, 'custom'), enabled: true, version: 9, mode: 'fill_first' }
    getPolicy.mockImplementation(async (group: number) => group === 0
      ? { policy: inherited, version: 9, group_id: 0, model: 'custom' }
      : { policy: null, version: 0, group_id: 2, model: 'custom' })
    const wrapper = await setup()
    await wrapper.get('[data-testid="scope-group"]').setValue(2)
    await wrapper.get('[data-testid="load-policy"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="policy-enabled"]').element.checked).toBe(true)
    expect(wrapper.get('[data-testid="policy-mode"]').element.value).toBe('fill_first')
    expect(wrapper.get('[data-testid="inherited-policy"]').text()).toContain('inheritedPolicy')
    expect(savePolicy).not.toHaveBeenCalled()
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(savePolicy).toHaveBeenCalledWith(expect.objectContaining({ group_id: 2, enabled: true, mode: 'fill_first', version: 0 }), 0)
  })
  it('shows global profile fallback for a local policy instead of claiming observe-only', async () => {
    const global = { ...createSchedulingPolicy(0, 'custom'), version: 9, profiles: [{ name: 'model-default', health_threshold_ms: 8000, recovery_threshold_ms: 6000, attempt_timeout_ms: 12000, total_budget_ms: 25000, min_attempt_window_ms: 8000 }] }
    getPolicy.mockImplementation(async (group: number) => group === 0
      ? { policy: global, version: 9, group_id: 0, model: 'custom' }
      : { policy: { ...createSchedulingPolicy(2, 'custom'), enabled: true, version: 2 }, version: 2, group_id: 2, model: 'custom' })
    const wrapper = await setup()
    await wrapper.get('[data-testid="scope-group"]').setValue(2)
    await wrapper.get('[data-testid="load-policy"]').trigger('click'); await flushPromises()
    expect(wrapper.find('[data-testid="observe-only"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="inherited-profiles"]').text()).toContain('model-default')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(savePolicy).toHaveBeenCalledWith(expect.objectContaining({ profiles: [] }), 2)
  })
  it('keeps endpoint protocols separate in both selectors and defaults preview to responses', async () => {
    const wrapper = await setup()
    const protocols = ['responses', 'chat', 'messages', 'gemini', 'ws', 'http']
    const selector = wrapper.get('[data-testid="explain-protocol"]')
    expect(selector.element.value).toBe('responses')
    expect(selector.findAll('option').map(option => option.attributes('value'))).toEqual(protocols)
    for (const protocol of protocols) {
      await selector.setValue(protocol)
      await wrapper.get('[data-testid="explain"]').trigger('click'); await flushPromises()
      expect(explain).toHaveBeenLastCalledWith(expect.objectContaining({ protocol }), expect.any(AbortSignal))
    }
    await wrapper.get('[data-testid="add-profile"]').trigger('click')
    const profile = wrapper.get('[data-testid="profile-0-transport"]')
    expect(profile.findAll('option').map(option => option.attributes('value'))).toEqual(['', ...protocols])
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(savePolicy.mock.calls[0][0].profiles[0].transport).toBe('')
    expect(wrapper.get('[data-testid="protocol-sampling-hint"]').text()).toContain('protocolSamplingHint')
    expect(englishScheduling.scheduling.protocolOptions.http).toContain('Other / unrecognized HTTP')
    expect(chineseScheduling.scheduling.protocolOptions.http).toContain('其他 / 未识别 HTTP')
    expect(englishScheduling.scheduling.protocolOptions.messages).toContain('/v1/messages')
    expect(englishScheduling.scheduling.protocolOptions.chat).toContain('/v1/chat/completions')
  })
  it('edits the legacy anthropic profile alias as messages without merging HTTP protocols', async () => {
    getPolicy.mockResolvedValue({ ...savedDocument(), policy: { ...savedDocument().policy, profiles: [{ name: 'legacy-messages', transport: 'anthropic', health_threshold_ms: 0, recovery_threshold_ms: 0, attempt_timeout_ms: 0, total_budget_ms: 0, min_attempt_window_ms: 0 }] } })
    const wrapper = await setup()
    expect(wrapper.get('[data-testid="profile-0-transport"]').element.value).toBe('messages')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(savePolicy.mock.calls[0][0].profiles[0].transport).toBe('messages')
  })
})
