import { useSchedulingModeStore } from '@/stores/schedulingMode'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { DOMWrapper, flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'

import AccountsView from '../AccountsView.vue'
import AccountActionMenu from '@/components/admin/account/AccountActionMenu.vue'

const {
  listAccounts,
  listWithEtag,
  getById,
  getBatchTodayStats,
  getUpstreamBillingProbeSettings,
  getAllProxies,
  getAllGroups,
  refreshCredentials,
  setSchedulable,
  bulkUpdate,
  showError,
  showWarning
} = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  listWithEtag: vi.fn(),
  getById: vi.fn(),
  getBatchTodayStats: vi.fn(),
  getUpstreamBillingProbeSettings: vi.fn(),
  getAllProxies: vi.fn(),
  getAllGroups: vi.fn(),
  refreshCredentials: vi.fn(),
  setSchedulable: vi.fn(),
  bulkUpdate: vi.fn(),
  showError: vi.fn(),
  showWarning: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      getById,
      listWithEtag,
      getBatchTodayStats,
      getUpstreamBillingProbeSettings,
      delete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn(),
      setSchedulable,
      bulkUpdate,
      refreshCredentials
    },
    proxies: { getAll: getAllProxies },
    groups: { getAll: getAllGroups }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showWarning, showSuccess: vi.fn(), showInfo: vi.fn() })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ token: 'test-token', isSimpleMode: false })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const DataTableStub = defineComponent({
  props: { data: { type: Array, default: () => [] } },
  template: `
    <div>
      <div v-for="row in data" :key="row.id" :data-account-name="row.name">
        <slot name="cell-groups" :row="row" />
        <slot name="cell-schedulable" :row="row" />
        <span data-test="select-account"><slot name="cell-select" :row="row" /></span>
        <slot name="cell-actions" :row="row" />
      </div>
    </div>
  `
})

const AccountGroupsCellStub = defineComponent({
  props: { groups: { type: Array, default: () => [] } },
  template: '<span data-test="account-groups">{{ groups.map(group => group.name).join(",") }}</span>'
})

const EditAccountModalStub = defineComponent({
  props: { show: Boolean, account: { type: Object, default: null } },
  template: '<div data-test="edit-account">{{ show ? account?.name : "" }}</div>'
})

const AccountTestModalStub = defineComponent({
  props: { show: Boolean, account: { type: Object, default: null } },
  template: '<div data-test="test-account">{{ show ? account?.name : "" }}</div>'
})

const AccountStatsModalStub = defineComponent({
  props: { show: Boolean, account: { type: Object, default: null } },
  template: '<div data-test="stats-account">{{ show ? account?.name : "" }}</div>'
})

function mountView(stubActionMenu = true) {
  return mount(AccountsView, {
    attachTo: document.body,
    global: {
      stubs: {
        RouterLink: { template: '<a><slot /></a>' },
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
        DataTable: DataTableStub,
        AccountTableActions: { emits: ['refresh'], template: '<div><button data-test="refresh-accounts" @click="$emit(\'refresh\')">Refresh</button><slot name="after" /></div>' },
        AccountTableFilters: true,
        AccountBulkActionsBar: true,
        Pagination: true,
        ConfirmDialog: true,
        AccountActionMenu: stubActionMenu,
        ImportDataModal: true,
        ReAuthAccountModal: true,
        AccountTestModal: AccountTestModalStub,
        AccountStatsModal: AccountStatsModalStub,
        ScheduledTestsPanel: true,
        SyncFromCrsModal: true,
        TempUnschedStatusModal: true,
        ErrorPassthroughRulesModal: true,
        TLSFingerprintProfilesModal: true,
        CreateAccountModal: true,
        EditAccountModal: EditAccountModalStub,
        BulkEditAccountModal: true,
        PlatformTypeBadge: true,
        AccountCapacityCell: true,
        AccountStatusIndicator: true,
        AccountTodayStatsCell: true,
        AccountGroupsCell: AccountGroupsCellStub,
        AccountUsageCell: true,
        UpstreamBillingRateCell: true,
        HelpTooltip: true,
        Icon: true,
        Teleport: stubActionMenu
      }
    }
  })
}

const listRow = {
  id: 42,
  name: 'compact row',
  platform: 'openai',
  type: 'oauth',
  status: 'active',
  schedulable: true,
  concurrency: 2,
  priority: 1,
  group_ids: [7],
  extra: {},
  credentials: {}
}

const fullAccount = {
  ...listRow,
  groups: [{ id: 7, name: 'codex', platform: 'openai' }],
  account_groups: [{ account_id: 42, group_id: 7 }],
  credentials: { api_key: 'redacted' },
  extra: { detail_only: true }
}

describe('admin AccountsView lite account list', () => {
  beforeEach(() => {
  useSchedulingModeStore().document = { mode: 'sub2api', version: 1 }
    localStorage.clear()
    listAccounts.mockReset().mockResolvedValue({ items: [listRow], total: 1, page: 1, page_size: 20, pages: 1 })
    listWithEtag.mockReset().mockResolvedValue({ notModified: true, etag: 'compact-etag', data: null })
    getById.mockReset().mockResolvedValue(fullAccount)
    getBatchTodayStats.mockReset().mockResolvedValue({ stats: {} })
    getUpstreamBillingProbeSettings.mockReset().mockResolvedValue({ enabled: true })
    getAllProxies.mockReset().mockResolvedValue([])
    getAllGroups.mockReset().mockResolvedValue([{ id: 7, name: 'codex', platform: 'openai' }])
    refreshCredentials.mockReset()
    setSchedulable.mockReset().mockImplementation(async (id, schedulable) => ({ id, schedulable }))
    bulkUpdate.mockReset()
    showError.mockReset()
    showWarning.mockReset()
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it.each(['controlled', 'sub2api'] as const)('toggles scheduling immediately without a dialog in %s mode', async mode => {
    useSchedulingModeStore().document = { mode, version: 1 }
    const wrapper = mountView()
    await flushPromises()
    const toggle = wrapper.get('button[role="switch"]')
    expect(toggle.attributes('type')).toBe('button')
    expect(toggle.attributes('aria-checked')).toBe('true')
    expect(toggle.attributes('aria-label')).toContain('compact row')
    await toggle.trigger('click'); await flushPromises()
    expect(setSchedulable).toHaveBeenLastCalledWith(42, false)
    expect(toggle.attributes('aria-checked')).toBe('false')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(wrapper.text()).not.toMatch(/controlTitle|diagnosticsTitle|failureDomains|globalAccountEnabled/)
    await toggle.trigger('click'); await flushPromises()
    expect(setSchedulable).toHaveBeenLastCalledWith(42, true)
    expect(toggle.attributes('aria-checked')).toBe('true')
    wrapper.unmount()
  })

  it('locks each pending account independently against rapid and interleaved clicks', async () => {
    listAccounts.mockResolvedValue({ items: [listRow, { ...listRow, id: 43, name: 'second' }], total: 2, page: 1, page_size: 20, pages: 1 })
    const pending = new Map<number, (value: unknown) => void>()
    setSchedulable.mockImplementation(id => new Promise(resolve => pending.set(id, resolve)))
    const wrapper = mountView(); await flushPromises()
    const toggles = wrapper.findAll('button[role="switch"]')
    const firstClick = toggles[0].trigger('click')
    const duplicateClick = toggles[0].trigger('click')
    await Promise.all([firstClick, duplicateClick])
    await toggles[1].trigger('click')
    await toggles[0].trigger('click')
    expect(setSchedulable).toHaveBeenCalledTimes(2)
    expect(toggles[0].attributes('aria-busy')).toBe('true')
    expect(toggles[1].attributes('disabled')).toBeDefined()
    pending.get(42)!({ id: 42, schedulable: false }); await flushPromises()
    expect(toggles[0].attributes('disabled')).toBeUndefined()
    expect(toggles[1].attributes('disabled')).toBeDefined()
    pending.get(43)!({ id: 43, schedulable: false }); await flushPromises()
    expect(toggles.every(toggle => toggle.attributes('aria-checked') === 'false')).toBe(true)
    wrapper.unmount()
  })

  it('keeps the confirmed state and allows retry after a failed scheduling update', async () => {
    setSchedulable.mockRejectedValueOnce(new Error('temporary failure'))
    const wrapper = mountView(); await flushPromises()
    const toggle = wrapper.get('button[role="switch"]')
    await toggle.trigger('click'); await flushPromises()
    expect(toggle.attributes('aria-checked')).toBe('true')
    expect(toggle.attributes('disabled')).toBeUndefined()
    expect(showError).toHaveBeenCalledWith('admin.accounts.failedToToggleSchedulable')
    await toggle.trigger('click'); await flushPromises()
    expect(setSchedulable).toHaveBeenCalledTimes(2)
    expect(toggle.attributes('aria-checked')).toBe('false')
    wrapper.unmount()
  })

  it('ignores an automatic list response that started before a completed account toggle', async () => {
    vi.useFakeTimers()
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    localStorage.setItem('account-auto-refresh', JSON.stringify({ enabled: true, interval_seconds: 5 }))
    let resolveList!: (value: unknown) => void
    listWithEtag.mockReturnValueOnce(new Promise(resolve => { resolveList = resolve }))
    const wrapper = mountView(); await flushPromises()
    await vi.advanceTimersByTimeAsync(6000)
    expect(listWithEtag).toHaveBeenCalledTimes(1)
    const toggle = wrapper.get('button[role="switch"]')
    await toggle.trigger('click'); await flushPromises()
    expect(toggle.attributes('aria-checked')).toBe('false')
    resolveList({ notModified: false, etag: 'stale-before-toggle', data: { items: [listRow], total: 1, pages: 1 } })
    await flushPromises()
    expect(toggle.attributes('aria-checked')).toBe('false')
    wrapper.unmount()
  })

  it('preserves the confirmed switch when a manual refresh returns an older account snapshot', async () => {
    const wrapper = mountView(); await flushPromises()
    let resolveList!: (value: unknown) => void
    listAccounts.mockReturnValueOnce(new Promise(resolve => { resolveList = resolve }))
    await wrapper.get('[data-test="refresh-accounts"]').trigger('click')
    const toggle = wrapper.get('button[role="switch"]')
    await toggle.trigger('click'); await flushPromises()
    expect(toggle.attributes('aria-checked')).toBe('false')
    resolveList({ items: [listRow], total: 1, page: 1, page_size: 20, pages: 1 })
    await flushPromises()
    expect(toggle.attributes('aria-checked')).toBe('false')
    wrapper.unmount()
  })

  it('prevents bulk and single-account scheduling writes from racing on selected accounts', async () => {
    listAccounts.mockResolvedValue({ items: [listRow, { ...listRow, id: 43, name: 'second' }], total: 2, page: 1, page_size: 20, pages: 1 })
    let finishSingle!: (value: unknown) => void
    let finishBulk!: (value: unknown) => void
    setSchedulable.mockReturnValueOnce(new Promise(resolve => { finishSingle = resolve }))
    bulkUpdate.mockReturnValueOnce(new Promise(resolve => { finishBulk = resolve }))
    const wrapper = mountView(); await flushPromises()
    for (const checkbox of wrapper.findAll('[data-test="select-account"] input')) await checkbox.setValue(true)
    const bar = wrapper.findComponent({ name: 'AccountBulkActionsBar' })
    const toggles = wrapper.findAll('button[role="switch"]')
    await toggles[0].trigger('click')
    bar.vm.$emit('toggle-schedulable', false); await flushPromises()
    expect(bulkUpdate).not.toHaveBeenCalled()
    finishSingle({ id: 42, schedulable: false }); await flushPromises()
    bar.vm.$emit('toggle-schedulable', true); await flushPromises()
    bar.vm.$emit('toggle-schedulable', false)
    await toggles[1].trigger('click')
    expect(bulkUpdate).toHaveBeenCalledTimes(1)
    expect(bulkUpdate).toHaveBeenCalledWith([42, 43], { schedulable: true })
    expect(setSchedulable).toHaveBeenCalledTimes(1)
    expect(toggles.every(toggle => toggle.attributes('disabled') !== undefined)).toBe(true)
    finishBulk({ success: 2, failed: 0 }); await flushPromises()
    expect(toggles.every(toggle => toggle.attributes('aria-checked') === 'true')).toBe(true)
    expect(toggles.every(toggle => toggle.attributes('disabled') === undefined)).toBe(true)
    wrapper.unmount()
  })

  it('keeps lite=1 on the initial list request', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(listAccounts).toHaveBeenCalledWith(
      1,
      20,
      expect.objectContaining({ lite: '1' }),
      expect.objectContaining({ signal: expect.any(AbortSignal) })
    )
    wrapper.unmount()
  })

  it('maps group_ids through the group catalog for the table cell', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-test="account-groups"]').text()).toBe('codex')
    wrapper.unmount()
  })

  it('keeps the action menu open during internal scrolling but closes it on table scrolling', async () => {
    const wrapper = mountView(false)
    await flushPromises()

    const trigger = wrapper.findAll('button').find(button => button.text() === 'common.more')!
    await trigger.trigger('click')
    const menu = new DOMWrapper(document.body.querySelector('.action-menu-content')!)
    menu.element.dispatchEvent(new Event('scroll'))
    await flushPromises()
    expect(wrapper.findComponent(AccountActionMenu).props('show')).toBe(true)

    menu.get('button').element.dispatchEvent(new Event('scroll'))
    await flushPromises()
    expect(wrapper.findComponent(AccountActionMenu).props('show')).toBe(true)

    wrapper.getComponent(DataTableStub).element.dispatchEvent(new Event('scroll'))
    await flushPromises()
    expect(wrapper.findComponent(AccountActionMenu).props('show')).toBe(false)
    wrapper.unmount()
  })

  it('keeps lite=1 on automatic ETag refreshes', async () => {
    vi.useFakeTimers()
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    localStorage.setItem('account-auto-refresh', JSON.stringify({ enabled: true, interval_seconds: 5 }))
    const wrapper = mountView()
    await flushPromises()

    await vi.advanceTimersByTimeAsync(6000)
    await flushPromises()

    expect(listWithEtag).toHaveBeenCalledWith(
      1,
      20,
      expect.objectContaining({ lite: '1' }),
      expect.objectContaining({ etag: null })
    )
    wrapper.unmount()
  })

  it('loads the full account by id before opening edit, test, and stats actions', async () => {
    const wrapper = mountView()
    await flushPromises()

    const editButton = wrapper.findAll('button').find(button => button.text().includes('common.edit'))
    expect(editButton).toBeTruthy()
    await editButton!.trigger('click')
    await flushPromises()
    expect(getById).toHaveBeenCalledWith(42)
    expect(wrapper.get('[data-test="edit-account"]').text()).toBe('compact row')

    const menu = wrapper.findComponent(AccountActionMenu)
    menu.vm.$emit('test', listRow)
    await flushPromises()
    expect(getById).toHaveBeenCalledTimes(2)
    expect(wrapper.get('[data-test="test-account"]').text()).toBe('compact row')

    menu.vm.$emit('stats', listRow)
    await flushPromises()
    expect(getById).toHaveBeenCalledTimes(3)
    expect(wrapper.get('[data-test="stats-account"]').text()).toBe('compact row')
    wrapper.unmount()
  })

  it('shows the warning and patches the account after a partial Antigravity refresh', async () => {
    refreshCredentials.mockResolvedValue({
      account: { ...fullAccount, name: 'refreshed account' },
      message: 'Token refreshed, but project_id is temporarily unavailable',
      warning: 'missing_project_id_temporary'
    })
    const wrapper = mountView(false)
    await flushPromises()

    wrapper.findComponent(AccountActionMenu).vm.$emit('refresh-token', listRow)
    await flushPromises()

    expect(refreshCredentials).toHaveBeenCalledWith(42)
    expect(wrapper.get('[data-account-name]').attributes('data-account-name')).toBe('refreshed account')
    expect(showWarning).toHaveBeenCalledWith('Token refreshed, but project_id is temporarily unavailable')
    wrapper.unmount()
  })

  it('shows an error and keeps the modal closed when detail loading fails', async () => {
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
    getById.mockRejectedValueOnce(new Error('detail failed'))
    const wrapper = mountView()
    await flushPromises()

    const editButton = wrapper.findAll('button').find(button => button.text().includes('common.edit'))
    await editButton!.trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('detail failed')
    expect(wrapper.get('[data-test="edit-account"]').text()).toBe('')
    consoleError.mockRestore()
    wrapper.unmount()
  })
})
