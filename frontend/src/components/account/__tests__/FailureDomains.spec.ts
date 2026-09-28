import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
const api = vi.hoisted(() => ({ getFailureDomains: vi.fn(), putFailureDomains: vi.fn(), permitFailureRecovery: vi.fn(), getUnknownAttempt: vi.fn(), resolveUnknownAttempt: vi.fn() }))
vi.mock('@/api/admin/schedulingDomains', () => api)
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
import AccountFailureDomains from '../AccountFailureDomains.vue'
import FailureGateRecovery from '../FailureGateRecovery.vue'
import UnknownAttemptResolution from '../UnknownAttemptResolution.vue'
const wrappers: ReturnType<typeof mount>[] = []
const domains = { account_id: 1, version: 8, quota_pool_id: 'org-a', availability_pool_id: '' }
beforeEach(() => { vi.resetAllMocks(); api.getFailureDomains.mockResolvedValue({ domains: { ...domains }, gates: [], eligible: true }) })
afterEach(() => wrappers.splice(0).forEach(w => w.unmount()))
describe('explicit failure controls', () => {
 it('loads only when expanded, submits domain CAS and offers refresh after conflict', async () => {
  const w = mount(AccountFailureDomains, { props: { accountId: 1 } }); wrappers.push(w)
  expect(api.getFailureDomains).not.toHaveBeenCalled()
  const details = w.get('details'); (details.element as HTMLDetailsElement).open = true; await details.trigger('toggle'); await flushPromises()
  await w.get('[data-testid="quota-pool-id"]').setValue('org-b')
  api.putFailureDomains.mockRejectedValueOnce(new Error('revision changed'))
  await w.get('button').trigger('click'); await flushPromises()
  expect(api.putFailureDomains).toHaveBeenCalledWith({ ...domains, quota_pool_id: 'org-b' })
  expect(w.get('[role="alert"]').exists()).toBe(true)
  await w.get('button').trigger('click'); await flushPromises(); expect(api.getFailureDomains).toHaveBeenCalledTimes(2)
 })
 it('discards old account reads when the editor changes account', async () => {
  let finish!: (v: unknown) => void
  api.getFailureDomains.mockReturnValueOnce(new Promise(resolve => { finish = resolve }))
  const w = mount(AccountFailureDomains, { props: { accountId: 1 } }); wrappers.push(w)
  const details = w.get('details'); (details.element as HTMLDetailsElement).open = true; await details.trigger('toggle')
  await w.setProps({ accountId: 2 }); await flushPromises()
  finish({ domains: { ...domains, quota_pool_id: 'stale' }, gates: [], eligible: true }); await flushPromises()
  expect((w.get('[data-testid="quota-pool-id"]').element as HTMLInputElement).value).toBe('org-a')
 })
 it('requires explicit recovery evidence and carries both version fences', async () => {
  api.permitFailureRecovery.mockResolvedValue({})
  const gate = { key: 'a'.repeat(64), scope: 'quota_pool', reason: 'quota_exhausted', version: 12, blocked: true, probe_active: false }
  const w = mount(FailureGateRecovery, { props: { accountId: 1, gate, model: 'upstream-model', domainVersion: 8 } }); wrappers.push(w)
  await w.get('form').trigger('submit'); expect(api.permitFailureRecovery).not.toHaveBeenCalled()
  await w.get('input[maxlength="512"]').setValue('provider receipt 123')
  await w.get('input[type="datetime-local"]').setValue('2026-09-28T09:00:00')
  await w.get('input[type="checkbox"]').setValue(true)
  await w.get('form').trigger('submit'); await flushPromises()
  expect(api.permitFailureRecovery).toHaveBeenCalledWith(1, expect.objectContaining({ gate_key: gate.key, model: 'upstream-model', expected_version: 12, domain_version: 8, reference: 'provider receipt 123' }))
  expect(w.emitted('recovered')).toHaveLength(1)
 })
 it('does not settle unknown without confirmation and does not accept a stale ticket read', async () => {
  let finish!: (v: unknown) => void
  api.getUnknownAttempt.mockReturnValueOnce(new Promise(resolve => { finish = resolve }))
  const w = mount(UnknownAttemptResolution, { props: { ticketId: 'first' } }); wrappers.push(w)
  expect(api.getUnknownAttempt).not.toHaveBeenCalled(); await w.get('button').trigger('click')
  await w.setProps({ ticketId: 'second' })
  finish({ ticket_id: 'first', state: 'unknown', version: 1, audit: [] }); await flushPromises()
  expect(w.find('input[type="checkbox"]').exists()).toBe(false)
  expect(api.resolveUnknownAttempt).not.toHaveBeenCalled()
 })
})

it('submits UNKNOWN evidence only after confirmation and requires a new read after conflict', async () => {
  api.getUnknownAttempt.mockResolvedValue({ ticket_id: 'unknown-one', account_id: 1, state: 'unknown', version: 9, audit: [], metrics: {} })
  const w = mount(UnknownAttemptResolution, { props: { ticketId: 'unknown-one' } }); wrappers.push(w)
  await w.get('button').trigger('click'); await flushPromises()
  await w.get('input[maxlength="512"]').setValue('verified remote terminal receipt')
  await w.get('input[type="datetime-local"]').setValue('2026-09-28T04:00:00')
  const buttons = w.findAll('button')
  expect(buttons[1].attributes('disabled')).toBeDefined()
  expect(api.resolveUnknownAttempt).not.toHaveBeenCalled()
  await w.get('input[type="checkbox"]').setValue(true)
  api.resolveUnknownAttempt.mockRejectedValueOnce({ response: { status: 409, data: { message: 'changed' } } })
  await buttons[1].trigger('click'); await flushPromises()
  expect(api.resolveUnknownAttempt).toHaveBeenCalledTimes(1)
  expect(api.resolveUnknownAttempt).toHaveBeenCalledWith('unknown-one', expect.objectContaining({ expected_version: 9, outcome: 'completed', usage_pending: true, reference: 'verified remote terminal receipt' }))
  expect(w.find('input[type="checkbox"]').exists()).toBe(false)
  expect(w.get('[role="alert"]').exists()).toBe(true)
  await w.get('button').trigger('click'); await flushPromises()
  expect(api.getUnknownAttempt).toHaveBeenCalledTimes(2)
  expect((w.get('input[type="checkbox"]').element as HTMLInputElement).checked).toBe(false)
})
