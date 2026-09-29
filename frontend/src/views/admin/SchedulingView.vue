<template>
  <AppLayout>
    <div v-if="!modeStore.isControlled" class="mx-auto max-w-3xl p-6">
      <section class="scheduling-card space-y-3" aria-live="polite">
        <template v-if="modeStore.isSub2API"><h1 class="text-lg font-semibold">{{ t('admin.scheduling.modeSettings.inactive') }}</h1><p class="text-sm text-gray-600 dark:text-dark-300">{{ t('admin.scheduling.modeSettings.inactiveHint') }}</p><RouterLink to="/admin/settings?tab=gateway" class="btn btn-primary">{{ t('admin.scheduling.modeSettings.settings') }}</RouterLink></template>
        <template v-else><p :role="modeStore.failed ? 'alert' : 'status'">{{ modeStore.failed ? t('admin.scheduling.modeSettings.loadFailed') : t('common.loading') }}</p><button v-if="modeStore.failed" type="button" class="btn btn-secondary" @click="modeStore.fetch(true)">{{ t('common.refresh') }}</button></template>
      </section>
    </div>
    <div v-else ref="pageRoot" class="mx-auto max-w-7xl space-y-5 p-4 sm:p-6">
      <header class="flex flex-wrap items-start justify-between gap-3">
        <div><h1 class="text-xl font-semibold">{{ t('admin.scheduling.title') }}</h1><p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.scheduling.groupPolicy.description') }}</p></div>
        <div class="flex flex-wrap items-center gap-2"><RouterLink to="/admin/accounts" class="btn btn-secondary">{{ t('admin.scheduling.manageAccounts') }}</RouterLink><button type="button" class="btn btn-primary" :disabled="!policy || loading || saving || conflicted || !dirty" data-testid="save-policy-top" @click="save">{{ saving ? t('common.saving') : t('common.save') }}</button></div>
      </header>
      <section class="scheduling-card">
        <div class="flex flex-wrap items-end gap-3">
          <label class="scheduling-label min-w-0 flex-1 sm:max-w-sm">{{ t('admin.scheduling.group') }}
            <select :value="scopeGroup" class="input mt-1" :disabled="saving || groupsLoading" data-testid="scope-group" @change="changeGroup">
              <option :value="-1" disabled>{{ t('admin.scheduling.groupPolicy.selectGroup') }}</option>
              <option :value="0">{{ t('admin.scheduling.groupPolicy.' + (defaultScope === 'all_accounts' ? 'allAccounts' : 'ungrouped')) }}</option>
              <option v-for="group in groups" :key="group.id" :value="group.id">{{ group.name }}</option>
            </select>
          </label>
          <button class="btn btn-secondary" :disabled="groupsLoading || loading || saving" data-testid="reload-policy" @click="requestReload">{{ t('admin.scheduling.groupPolicy.reload') }}</button>
          <span v-if="policy" class="text-xs text-gray-500">{{ t('admin.scheduling.version', { value: loadedVersion }) }}</span>
        </div>
        <p class="mt-3 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.scheduling.groupPolicy.scopeHint') }}</p><p v-if="policy && !configured" class="mt-2 text-sm text-gray-600 dark:text-dark-300">{{ t('admin.scheduling.groupPolicy.defaultNote') }}</p>
      </section>
      <p v-if="error" role="alert" class="rounded-xl bg-red-50 p-3 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-300">{{ error }}</p>
      <p v-if="notice" ref="noticeElement" role="status" class="rounded-xl bg-emerald-50 p-3 text-sm text-emerald-700 dark:bg-emerald-950/30 dark:text-emerald-300">{{ notice }}</p>
      <div v-if="loading" role="status" class="scheduling-card space-y-3" :aria-label="t('common.loading')"><div class="h-5 w-40 rounded bg-gray-100 dark:bg-dark-700"></div><div v-for="row in 3" :key="row" class="h-12 rounded bg-gray-100 dark:bg-dark-700"></div><span class="sr-only">{{ t('common.loading') }}</span></div>
      <details v-if="migrationWarnings.length" class="text-sm text-gray-600 dark:text-dark-300"><summary class="cursor-pointer">{{ t('admin.scheduling.groupPolicy.historyNotes') }}</summary><ul class="mt-2 list-inside list-disc"><li v-for="warning in migrationWarnings" :key="warning.code">{{ warning.message }}</li></ul></details>
      <form v-if="policy" class="space-y-5" @submit.prevent="save">
        <fieldset :disabled="saving || loading || !modeStore.isControlled" class="min-w-0 space-y-5">
          <section class="scheduling-card">
            <h2 class="font-semibold">{{ t('admin.scheduling.groupPolicy.accountTitle') }}</h2>
            <p class="mt-2 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.scheduling.groupPolicy.accountHint') }}</p>
            <div class="mt-4 flex flex-wrap items-end gap-3">
              <label class="scheduling-label min-w-40 flex-1">{{ t('admin.scheduling.groupPolicy.search') }}<input v-model.trim="accountSearch" class="input mt-1" type="search" data-testid="account-search"></label>
              <label v-if="selectedAccounts.length" class="scheduling-label w-36">{{ t('admin.scheduling.groupPolicy.batchPriority') }}<input v-model="batchPriority" class="input mt-1" type="number" step="1" :placeholder="t('admin.scheduling.groupPolicy.noChange')" data-testid="batch-priority"></label>
              <label v-if="selectedAccounts.length" class="scheduling-label w-36">{{ t('admin.scheduling.groupPolicy.batchWeight') }}<input v-model="batchWeight" class="input mt-1" type="number" min="0" max="1000000" step="1" :placeholder="t('admin.scheduling.groupPolicy.noChange')" data-testid="batch-weight"></label>
              <label class="flex w-full items-center gap-2 text-sm sm:hidden"><input type="checkbox" :checked="allVisibleSelected" data-testid="select-visible-mobile" @change="selectVisible">{{ t('admin.scheduling.groupPolicy.selectVisible') }}</label>
              <button v-if="selectedAccounts.length" type="button" class="btn btn-secondary" :disabled="batchPriority === '' && batchWeight === ''" data-testid="apply-batch" @click="applyBatch">{{ t('admin.scheduling.groupPolicy.applyBatch', { count: selectedAccounts.length }) }}</button><button v-if="selectedAccounts.length" type="button" class="btn btn-secondary" @click="selectedAccounts = []">{{ t('admin.scheduling.groupPolicy.clearSelection') }}</button>
            </div>
            <div class="mt-4 overflow-x-auto">
              <table class="scheduling-account-table w-full text-left text-sm">
                <thead><tr class="border-b border-gray-200 dark:border-dark-600">
                  <th class="p-2"><input type="checkbox" :checked="allVisibleSelected" :aria-label="t('admin.scheduling.groupPolicy.selectVisible')" data-testid="select-visible" @change="selectVisible"></th>
                  <th class="p-2">{{ t('admin.scheduling.account') }}</th><th class="p-2">{{ t('admin.scheduling.priority') }}</th><th class="p-2">{{ t('admin.scheduling.weight') }}</th><th class="p-2">{{ t('admin.scheduling.groupPolicy.status') }}</th>
                </tr></thead>
                <tbody><tr v-for="account in visibleAccounts" :key="account.account_id" class="border-b border-gray-100 last:border-0 dark:border-dark-700" :data-testid="'account-row-' + account.account_id">
                  <td class="p-2"><input v-model="selectedAccounts" type="checkbox" :value="account.account_id" :aria-label="t('admin.scheduling.groupPolicy.selectNamed', { name: accountName(account.account_id) })"></td>
                  <td class="min-w-36 p-2"><div class="font-medium">{{ accountName(account.account_id) }}</div><div class="mt-1 text-xs text-gray-500">#{{ account.account_id }}<span v-if="accountDetails[account.account_id]?.platform"> · {{ accountDetails[account.account_id].platform }}</span></div></td>
                  <td class="p-2" :data-label="t('admin.scheduling.priority')"><input v-model.number="account.priority" type="number" step="1" min="-2147483648" max="2147483647" required class="input w-28" :aria-label="accountName(account.account_id) + ' ' + t('admin.scheduling.priority')" :data-testid="'priority-' + account.account_id"></td>
                  <td class="p-2" :data-label="t('admin.scheduling.weight')"><input v-model.number="account.traffic_weight" type="number" step="1" min="0" max="1000000" required class="input w-28" :aria-label="accountName(account.account_id) + ' ' + t('admin.scheduling.weight')" :data-testid="'weight-' + account.account_id"></td>
                  <td class="min-w-36 p-2 text-gray-600 dark:text-dark-300">{{ accountStatus(account.account_id, account.traffic_weight) }}</td>
                </tr></tbody>
              </table>
              <div v-if="!visibleAccounts.length" class="space-y-3 py-6 text-center text-sm text-gray-600 dark:text-dark-300" data-testid="empty-accounts"><p>{{ t('admin.scheduling.groupPolicy.' + (accountSearch ? 'emptyAccounts' : 'emptyGroup')) }}</p><button v-if="accountSearch" type="button" class="btn btn-secondary" @click="accountSearch = ''">{{ t('admin.scheduling.groupPolicy.clearSearch') }}</button><RouterLink v-else to="/admin/accounts" class="btn btn-secondary">{{ t('admin.scheduling.manageAccounts') }}</RouterLink></div>
            </div>
            <div v-if="pageCount > 1" class="mt-4 flex flex-wrap items-center justify-between gap-3 text-sm"><span>{{ t('admin.scheduling.groupPolicy.pageSummary', { page: pageNumber, pages: pageCount, count: filteredAccounts.length }) }}</span><div class="flex gap-2"><button type="button" class="btn btn-secondary" :disabled="pageNumber <= 1" @click="pageNumber--">{{ t('admin.scheduling.groupPolicy.previous') }}</button><button type="button" class="btn btn-secondary" :disabled="pageNumber >= pageCount" @click="pageNumber++">{{ t('admin.scheduling.groupPolicy.next') }}</button></div></div>
            <p class="mt-3 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.scheduling.groupPolicy.weightHint') }}</p>
          </section>
          <section class="scheduling-card">
            <h2 class="font-semibold">{{ t('admin.scheduling.groupPolicy.waitTitle') }}</h2>
            <div class="mt-4 grid gap-4 sm:grid-cols-2">
              <label class="scheduling-label">{{ t('admin.scheduling.groupPolicy.firstOutputWait') }}<div class="mt-1 flex items-center gap-2"><input :value="policy.first_output_timeout_ms / 1000" class="input" type="number" min="1" max="3600" step="1" required data-testid="first-output-wait" @input="changeFirstWait"><span>{{ t('admin.scheduling.groupPolicy.seconds') }}</span></div></label>
              <label class="scheduling-label">{{ t('admin.scheduling.groupPolicy.maxAttempts') }}<div class="mt-1 flex items-center gap-2"><input v-model.number="policy.max_attempts" class="input" type="number" min="1" max="10" step="1" required data-testid="max-attempts"><span>{{ t('admin.scheduling.groupPolicy.accountsUnit') }}</span></div></label>
            </div>
            <p class="mt-3 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.scheduling.groupPolicy.waitHint') }}</p>
            <details class="mt-4 border-t border-gray-200 pt-4 dark:border-dark-700"><summary class="cursor-pointer text-sm font-medium">{{ t('admin.scheduling.groupPolicy.advancedWait') }}</summary>
              <label class="mt-3 flex items-center gap-2 text-sm"><input v-model="automaticTotal" type="checkbox" data-testid="automatic-total" @change="updateAutomaticTotal">{{ t('admin.scheduling.groupPolicy.automaticTotal') }}</label>
              <label class="scheduling-label mt-3 block max-w-sm">{{ t('admin.scheduling.groupPolicy.totalWait') }}<div class="mt-1 flex items-center gap-2"><input :value="policy.total_wait_timeout_ms / 1000" :disabled="automaticTotal" class="input" type="number" :min="policy.first_output_timeout_ms / 1000" max="7200" step="1" required data-testid="total-wait" @input="policy.total_wait_timeout_ms = secondsToMilliseconds($event)"><span>{{ t('admin.scheduling.groupPolicy.seconds') }}</span></div></label>
            </details>
          </section>
          <div class="flex flex-wrap items-center justify-between gap-3 border-t border-gray-200 py-4 dark:border-dark-700">
            <p class="text-sm text-gray-500 dark:text-dark-400" data-testid="draft-status">{{ t('admin.scheduling.groupPolicy.' + (dirty ? 'unsaved' : 'upToDate')) }}</p>
            <button type="submit" class="btn btn-primary" :disabled="saving || conflicted || !dirty" data-testid="save-policy">{{ saving ? t('common.loading') : t('common.save') }}</button>
          </div>
        </fieldset>
      </form>
      <BaseDialog :show="pendingGroup !== null || pendingReload || pendingNavigation !== null" :title="t('admin.scheduling.groupPolicy.unsaved')" width="narrow" @close="keepDraft">
        <section role="alertdialog" :aria-label="t('admin.scheduling.groupPolicy.unsaved')"><p class="mt-3 text-sm text-gray-600 dark:text-dark-300">{{ t('admin.scheduling.groupPolicy.discardHint') }}</p>
          <div class="mt-5 flex justify-end gap-3"><button class="btn btn-secondary" data-testid="keep-draft" @click="keepDraft">{{ t('admin.scheduling.groupPolicy.keepEditing') }}</button><button class="btn btn-primary" data-testid="discard-draft" @click="discardDraft">{{ t('admin.scheduling.groupPolicy.discard') }}</button></div>
        </section>
      </BaseDialog>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { onBeforeRouteLeave, useRoute } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { useSchedulingModeStore } from '@/stores/schedulingMode'
import { useSchedulingFeedback } from '@/composables/useSchedulingFeedback'
import schedulingAPI from '@/api/admin/scheduling'
import accountsAPI from '@/api/admin/accounts'
import groupsAPI from '@/api/admin/groups'
import type { AccountListItem, AdminGroup } from '@/types'
import type { GroupSchedulingDocument, GroupSchedulingMigrationWarning, GroupSchedulingPolicy } from '@/types/scheduling'
import { cloneGroupPolicy, groupPolicyFingerprint, isGroupSchedulingConflict, validateGroupPolicy } from '@/utils/groupScheduling'
import { extractApiErrorMessage } from '@/utils/apiError'

const { t } = useI18n()
const route = useRoute()
const modeStore = useSchedulingModeStore()
const pageRoot = ref<HTMLElement | null>(null)
const noticeElement = ref<HTMLElement | null>(null)
const confirmFeedback = useSchedulingFeedback(pageRoot)
const displayOrder = ref<number[]>([])
const groups = ref<AdminGroup[]>([])
const groupsLoading = ref(true)
const scopeGroup = ref(-1)
const defaultScope = ref<GroupSchedulingDocument['default_scope']>('ungrouped')
const policy = ref<GroupSchedulingPolicy | null>(null)
const loadedVersion = ref(0)
const configured = ref(true)
const baseline = ref('')
const loading = ref(false)
const saving = ref(false)
const conflicted = ref(false)
const error = ref('')
const notice = ref('')
const migrationWarnings = ref<GroupSchedulingMigrationWarning[]>([])
const accountDetails = ref<Record<number, AccountListItem>>({})
const selectedAccounts = ref<number[]>([])
const accountSearch = ref('')
const pageNumber = ref(1)
const pageSize = 50
const batchPriority = ref('')
const batchWeight = ref('')
const automaticTotal = ref(true)
const pendingGroup = ref<number | null>(null)
const pendingReload = ref(false)
const pendingNavigation = ref<((allow: boolean) => void) | null>(null)
let loadGeneration = 0
let requests = new AbortController()
let alive = true
let initializingGroups = false
const dirty = computed(() => policy.value !== null && groupPolicyFingerprint(policy.value) !== baseline.value)
watch(dirty, changed => { if (changed) notice.value = '' })
const filteredAccounts = computed(() => {
  const query = accountSearch.value.toLowerCase()
  return (policy.value?.accounts ?? []).filter(account => !query || (accountName(account.account_id) + ' ' + account.account_id).toLowerCase().includes(query)).slice().sort((a, b) => displayOrder.value.indexOf(a.account_id) - displayOrder.value.indexOf(b.account_id))
})
const pageCount = computed(() => Math.max(1, Math.ceil(filteredAccounts.value.length / pageSize)))
const visibleAccounts = computed(() => filteredAccounts.value.slice((pageNumber.value - 1) * pageSize, pageNumber.value * pageSize))
watch(accountSearch, () => { pageNumber.value = 1 })
const allVisibleSelected = computed(() => visibleAccounts.value.length > 0 && visibleAccounts.value.every(account => selectedAccounts.value.includes(account.account_id)))
function accountName(id: number) { return accountDetails.value[id]?.name || t('admin.scheduling.groupPolicy.accountNumber', { id }) }
function accountStatus(id: number, weight: number) {
  if (weight === 0) return t('admin.scheduling.zeroWeight')
  const account = accountDetails.value[id]
  if (!account) return t('admin.scheduling.groupPolicy.statusUnknown')
  if (account.schedulable === false) return t('admin.scheduling.groupPolicy.disabled')
  if (account.status !== 'active') return t('admin.scheduling.groupPolicy.unavailable')
  return t('admin.scheduling.groupPolicy.available')
}
function secondsToMilliseconds(event: Event) { return Number((event.target as HTMLInputElement).value) * 1000 }
function changeFirstWait(event: Event) {
  if (!policy.value) return
  policy.value.first_output_timeout_ms = secondsToMilliseconds(event)
  updateAutomaticTotal()
}
function updateAutomaticTotal() { if (policy.value && automaticTotal.value) policy.value.total_wait_timeout_ms = policy.value.first_output_timeout_ms * 2 }
function selectVisible(event: Event) {
  const ids = visibleAccounts.value.map(account => account.account_id)
  selectedAccounts.value = (event.target as HTMLInputElement).checked ? [...new Set([...selectedAccounts.value, ...ids])] : selectedAccounts.value.filter(id => !ids.includes(id))
}
function applyBatch() {
  if (!policy.value || !selectedAccounts.value.length || (batchPriority.value === '' && batchWeight.value === '')) return
  const next = cloneGroupPolicy(policy.value)
  for (const account of next.accounts) if (selectedAccounts.value.includes(account.account_id)) {
    if (batchPriority.value !== '') account.priority = Number(batchPriority.value)
    if (batchWeight.value !== '') account.traffic_weight = Number(batchWeight.value)
  }
  const validation = validateGroupPolicy(next)
  if (validation) { error.value = t('admin.scheduling.groupPolicy.' + validation); return }
  policy.value = next
  error.value = ''; notice.value = ''; batchPriority.value = ''; batchWeight.value = ''
}
function changeGroup(event: Event) {
  const select = event.target as HTMLSelectElement
  const id = Number(select.value)
  select.value = String(scopeGroup.value)
  if (id === scopeGroup.value || !Number.isSafeInteger(id) || id < 0) return
  if (dirty.value) { pendingGroup.value = id; return }
  void loadGroup(id)
}
function requestReload() {
  if (scopeGroup.value < 0) { void initializeGroups(); return }
  if (dirty.value) { pendingReload.value = true; return }
  void loadGroup(scopeGroup.value)
}
function keepDraft() {
  pendingGroup.value = null; pendingReload.value = false
  const resolve = pendingNavigation.value; pendingNavigation.value = null; resolve?.(false)
}
function discardDraft() {
  const id = pendingGroup.value ?? scopeGroup.value
  const shouldReload = pendingGroup.value !== null || pendingReload.value
  pendingGroup.value = null; pendingReload.value = false
  const resolve = pendingNavigation.value; pendingNavigation.value = null
  if (resolve) resolve(true)
  else if (shouldReload) void loadGroup(id)
}
function acceptDocument(document: GroupSchedulingDocument, groupID: number) {
  if (!document.policy || document.policy.group_id !== groupID || !Array.isArray(document.policy.accounts)) throw new Error(t('admin.scheduling.groupPolicy.invalidScope'))
  policy.value = cloneGroupPolicy(document.policy)
  loadedVersion.value = document.version
  configured.value = document.configured !== false
  baseline.value = groupPolicyFingerprint(policy.value)
  displayOrder.value = [...policy.value.accounts].sort((a, b) => a.priority - b.priority || a.account_id - b.account_id).map(account => account.account_id)
  automaticTotal.value = policy.value.total_wait_timeout_ms === policy.value.first_output_timeout_ms * 2
  migrationWarnings.value = document.migration_warnings ?? []
  if (groupID === 0 && document.default_scope) defaultScope.value = document.default_scope
}
async function loadAccountDetails(groupID: number, document: GroupSchedulingDocument, signal: AbortSignal): Promise<Record<number, AccountListItem>> {
  const allowed = new Set(document.policy.accounts.map(account => account.account_id))
  const result: Record<number, AccountListItem> = {}
  let seen = 0
  for (let page = 1; allowed.size > Object.keys(result).length; page++) {
    const group = groupID > 0 ? String(groupID) : document.default_scope === 'all_accounts' ? undefined : 'ungrouped'
    const response = await accountsAPI.list(page, 100, { group, lite: 'true' }, { signal })
    for (const account of response.items) if (allowed.has(account.id)) result[account.id] = account
    seen += response.items.length
    if (!response.items.length || seen >= response.total || signal.aborted) break
  }
  return result
}
async function loadGroup(groupID: number) {
  const generation = ++loadGeneration
  requests.abort(); requests = new AbortController()
  scopeGroup.value = groupID; loading.value = true; policy.value = null; accountDetails.value = {}
  baseline.value = ''; selectedAccounts.value = []; accountSearch.value = ''; pageNumber.value = 1; conflicted.value = false; error.value = ''; notice.value = ''; migrationWarnings.value = []
  try {
    const document = await schedulingAPI.getGroupPolicy(groupID, requests.signal)
    if (!alive || generation !== loadGeneration) return
    acceptDocument(document, groupID)
    try {
      const details = await loadAccountDetails(groupID, document, requests.signal)
      if (alive && generation === loadGeneration) accountDetails.value = details
    } catch (cause) {
      if (alive && generation === loadGeneration && !requests.signal.aborted) notice.value = t('admin.scheduling.groupPolicy.namesUnavailable')
    }
  } catch (cause) {
    if (alive && generation === loadGeneration && !requests.signal.aborted) error.value = extractApiErrorMessage(cause, t('admin.scheduling.loadFailed'))
  } finally { if (alive && generation === loadGeneration) loading.value = false }
}
async function save() {
  if (!modeStore.isControlled || !policy.value || saving.value || conflicted.value || !dirty.value || policy.value.group_id !== scopeGroup.value) return
  const validation = validateGroupPolicy(policy.value)
  if (validation) { error.value = t('admin.scheduling.groupPolicy.' + validation); return }
  const groupID = scopeGroup.value
  const generation = loadGeneration
  saving.value = true; error.value = ''; notice.value = ''
  try {
    const document = await schedulingAPI.saveGroupPolicy(cloneGroupPolicy(policy.value), loadedVersion.value)
    if (!alive || generation !== loadGeneration) return
    acceptDocument(document, groupID)
    notice.value = t('admin.scheduling.saved', { version: loadedVersion.value })
    await nextTick(); confirmFeedback(noticeElement.value)
  } catch (cause) {
    if (!alive || generation !== loadGeneration) return
    conflicted.value = isGroupSchedulingConflict(cause)
    error.value = conflicted.value ? t('admin.scheduling.policyConflict') : extractApiErrorMessage(cause, t('admin.scheduling.saveFailed'))
  } finally { if (alive) saving.value = false }
}
function saveShortcut(event: KeyboardEvent) { if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 's' && modeStore.isControlled) { event.preventDefault(); void save() } }
function beforeUnload(event: BeforeUnloadEvent) { if (dirty.value) { event.preventDefault(); event.returnValue = '' } }
onBeforeRouteLeave(() => {
  if (saving.value) return false
  if (!dirty.value) return true
  return new Promise<boolean>(resolve => { pendingNavigation.value = resolve })
})
async function initializeGroups() {
  if (initializingGroups || groups.value.length || policy.value) return
  initializingGroups = true
  groupsLoading.value = true
  error.value = ''
  try {
    groups.value = await groupsAPI.getAllIncludingInactive()
    if (!alive) return
    const query = route.query.group_id ?? route.query.group
    const requested = query === undefined ? -1 : Number(query)
    const initial = requested === 0 || groups.value.some(group => group.id === requested) ? requested : groups.value[0]?.id ?? 0
    await loadGroup(initial)
  } catch (cause) { if (alive) error.value = extractApiErrorMessage(cause, t('admin.scheduling.loadFailed')) }
  finally { initializingGroups = false; if (alive) groupsLoading.value = false }
}
watch(() => modeStore.isControlled, value => { if (value) void initializeGroups(); else { requests.abort() } })
onMounted(async () => {
  window.addEventListener('beforeunload', beforeUnload)
  window.addEventListener('keydown', saveShortcut)
  await modeStore.fetch()
  if (modeStore.isControlled) void initializeGroups()
})
onUnmounted(() => { alive = false; loadGeneration++; requests.abort(); window.removeEventListener('beforeunload', beforeUnload); window.removeEventListener('keydown', saveShortcut); pendingNavigation.value?.(false) })
</script>

<style scoped>
.scheduling-card { @apply rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-800 sm:p-5; }
.scheduling-label { @apply text-sm font-medium text-gray-700 dark:text-dark-200; }
@media (max-width: 639px) {
  .scheduling-account-table, .scheduling-account-table tbody { display: block; }
  .scheduling-account-table thead { display: none; }
  .scheduling-account-table tbody tr { position: relative; display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); padding: 8px 0; }
  .scheduling-account-table td { min-width: 0; }
  .scheduling-account-table td:first-child { position: absolute; top: 8px; right: 0; }
  .scheduling-account-table td:nth-child(2) { grid-column: 1 / -1; padding-right: 36px; }
  .scheduling-account-table td[data-label]::before { content: attr(data-label); display: block; margin-bottom: 6px; font-size: 12px; }
  .scheduling-account-table td input[type=number] { width: 100%; font-size: 16px; }
  .scheduling-account-table td:nth-child(5) { font-size: 12px; }
}
</style>
