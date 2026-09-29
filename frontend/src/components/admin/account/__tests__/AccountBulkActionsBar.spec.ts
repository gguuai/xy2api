import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import AccountBulkActionsBar from '../AccountBulkActionsBar.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

describe('AccountBulkActionsBar', () => {
  it('disables both scheduling actions during a pending selected-account update', async () => {
    const wrapper = mount(AccountBulkActionsBar, {
      props: { selectedIds: [1], totalResults: 1, selectingAll: false, allResultsSelected: true, schedulingBusy: true }
    })
    const buttons = wrapper.findAll('button').filter(button => /enableScheduling|disableScheduling/.test(button.text()))
    expect(buttons).toHaveLength(2)
    for (const button of buttons) {
      expect(button.attributes('disabled')).toBeDefined()
      expect(button.attributes('aria-busy')).toBe('true')
      await button.trigger('click')
    }
    expect(wrapper.emitted('toggle-schedulable')).toBeUndefined()
    await wrapper.setProps({ schedulingBusy: false })
    await buttons[0].trigger('click')
    expect(wrapper.emitted('toggle-schedulable')).toEqual([[true]])
    wrapper.unmount()
  })

  it('allows selecting all results before any row is selected', async () => {
    const wrapper = mount(AccountBulkActionsBar, {
      props: {
        selectedIds: [],
        totalResults: 45,
        selectingAll: false,
        allResultsSelected: false
      }
    })

    const button = wrapper.findAll('button').find(item =>
      item.text().includes('admin.accounts.bulkActions.selectAllResults')
    )

    expect(button).toBeDefined()
    await button!.trigger('click')
    expect(wrapper.emitted('select-all-results')).toHaveLength(1)
  })

  it('preserves the upstream billing probe action from v0.1.166', async () => {
    const wrapper = mount(AccountBulkActionsBar, {
      props: {
        selectedIds: [1],
        totalResults: 45,
        selectingAll: false,
        allResultsSelected: false
      }
    })

    const button = wrapper.findAll('button').find(item =>
      item.text().includes('admin.accounts.bulkActions.probeUpstreamBilling')
    )

    expect(button).toBeDefined()
    await button!.trigger('click')
    expect(wrapper.emitted('probe-upstream-billing')).toHaveLength(1)
  })
})
