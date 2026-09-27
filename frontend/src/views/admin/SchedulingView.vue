<template>
  <AppLayout>
    <div class="mx-auto max-w-7xl space-y-5 p-4 sm:p-6">
      <header class="flex flex-wrap items-start justify-between gap-3">
        <div><h1 class="text-xl font-semibold">{{ t('admin.scheduling.title') }}</h1><p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.scheduling.description') }}</p></div>
        <RouterLink to="/admin/accounts" class="btn btn-secondary">{{ t('admin.scheduling.manageAccounts') }}</RouterLink>
      </header>
      <section class="scheduling-card">
        <div class="grid items-end gap-3 sm:grid-cols-3">
          <label class="scheduling-label">{{ t('admin.scheduling.group') }}<select v-model.number="scopeGroup" class="input mt-1" :disabled="saving" data-testid="scope-group"><option :value="0">{{ t('admin.scheduling.modelDefault') }}</option><option v-for="group in groups" :key="group.id" :value="group.id">{{ group.name }}</option></select></label>
          <label class="scheduling-label">{{ t('admin.scheduling.model') }}<input v-model.trim="scopeModel" class="input mt-1" :disabled="saving" :placeholder="t('admin.scheduling.modelPlaceholder')" data-testid="scope-model" @keyup.enter="loadPolicy"></label>
          <button class="btn btn-secondary" :disabled="loading || saving || !scopeModel" data-testid="load-policy" @click="loadPolicy">{{ loading ? t('common.loading') : t('admin.scheduling.loadPolicy') }}</button>
        </div>
        <p class="mt-3 text-xs text-gray-500">{{ t('admin.scheduling.precedence') }}</p>
      </section>
      <p v-if="error" role="alert" class="rounded-xl bg-red-50 p-3 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-300">{{ error }}</p>
      <p v-if="notice" role="status" class="rounded-xl bg-emerald-50 p-3 text-sm text-emerald-700 dark:bg-emerald-950/30 dark:text-emerald-300">{{ notice }}</p>
      <form v-if="policy" class="space-y-5" @submit.prevent="save">
        <fieldset :disabled="saving" class="space-y-5">
        <section class="scheduling-card">
          <div class="flex flex-wrap items-center justify-between gap-3"><h2 class="font-semibold">{{ t('admin.scheduling.routing') }}</h2><span class="text-xs text-gray-500">{{ t('admin.scheduling.version', { value: loadedVersion }) }}</span></div>
          <p v-if="inheritedPolicyVersion !== null" class="mt-3 rounded-lg bg-blue-50 p-3 text-sm text-blue-700 dark:bg-blue-950/30 dark:text-blue-300" data-testid="inherited-policy">{{ t('admin.scheduling.inheritedPolicy', { version: inheritedPolicyVersion }) }}</p>
          <label class="mt-4 flex items-center gap-2 text-sm font-medium"><input v-model="policy.enabled" type="checkbox" data-testid="policy-enabled">{{ t('admin.scheduling.enablePolicy') }}</label>
          <p class="mt-2 text-sm" :class="policy.enabled ? 'text-primary-600' : 'text-gray-500'" data-testid="mode-status">{{ policy.enabled ? t('admin.scheduling.enabledHint') : t('admin.scheduling.legacyHint') }}</p>
          <div class="mt-4 grid gap-4 sm:grid-cols-3">
            <label class="scheduling-label">{{ t('admin.scheduling.mode') }}<select v-model="policy.mode" class="input mt-1" data-testid="policy-mode"><option v-for="mode in modes" :key="mode" :value="mode">{{ t('admin.scheduling.modes.' + mode) }}</option></select></label>
            <label class="scheduling-label">{{ t('admin.scheduling.overflow') }}<select v-model="policy.overflow" class="input mt-1"><option value="immediate">{{ t('admin.scheduling.immediate') }}</option><option value="wait">{{ t('admin.scheduling.wait') }}</option></select></label>
            <label class="scheduling-label">{{ t('admin.scheduling.allDegraded') }}<select v-model="policy.all_degraded" class="input mt-1"><option v-for="mode in degradedModes" :key="mode" :value="mode">{{ t('admin.scheduling.degradedModes.' + mode) }}</option></select></label>
          </div>
          <label v-if="policy.overflow === 'wait'" class="scheduling-label mt-4 block max-w-xs">{{ t('admin.scheduling.queueWait') }}<input :value="policy.queue_wait_ms / 1000" type="number" min="0.001" step="0.001" class="input mt-1" @input="policy.queue_wait_ms = milliseconds($event)"></label>
          <div v-if="policy.mode === 'pin'" class="mt-4 grid gap-3 sm:grid-cols-2">
            <label class="scheduling-label">{{ t('admin.scheduling.pinAccount') }}<select v-model.number="policy.pin_account_id" class="input mt-1"><option :value="0">{{ t('admin.scheduling.selectAccount') }}</option><option v-for="account in scopedAccounts" :key="account.id" :value="account.id">{{ account.name }} (#{{ account.id }})</option></select></label>
            <label class="flex items-center gap-2 text-sm"><input v-model="policy.pin_fallback" type="checkbox">{{ t('admin.scheduling.pinFallback') }}</label>
          </div>
        </section>

        <section class="scheduling-card">
          <h2 class="font-semibold">{{ t('admin.scheduling.accountRules') }}</h2>
          <p class="mt-2 text-sm text-gray-500">{{ t('admin.scheduling.accountRulesHint') }}</p>
          <div class="mt-4 flex flex-wrap gap-2">
            <select v-model.number="addAccountID" class="input min-w-0 flex-1" :aria-label="t('admin.scheduling.selectAccount')" data-testid="add-account-select"><option :value="0">{{ t('admin.scheduling.selectAccount') }}</option><option v-for="account in availableAccounts" :key="account.id" :value="account.id">{{ account.name }} (#{{ account.id }})</option></select>
            <button type="button" class="btn btn-secondary" :disabled="!addAccountID" data-testid="add-account" @click="addAccount">{{ t('admin.scheduling.addAccount') }}</button>
          </div>
          <div v-if="policy.accounts.length" class="mt-4 overflow-x-auto">
            <table class="w-full text-left text-sm"><thead><tr class="border-b border-gray-200 dark:border-dark-600"><th class="p-2">{{ t('admin.scheduling.account') }}</th><th class="p-2">{{ t('admin.scheduling.priority') }}</th><th class="p-2">{{ t('admin.scheduling.weight') }}</th><th class="p-2">{{ t('admin.scheduling.hardConcurrency') }}</th><th v-if="policy.mode === 'fill_first'" class="p-2">{{ t('admin.scheduling.fillOrder') }}</th><th class="p-2">{{ t('admin.scheduling.controlTitle') }}</th><th class="p-2"><span class="sr-only">{{ t('common.delete') }}</span></th></tr></thead>
              <tbody><tr v-for="(rule, index) in policy.accounts" :key="rule.account_id" class="border-b border-gray-100 dark:border-dark-700">
                <td class="p-2"><div class="font-medium">{{ accountName(rule.account_id) }}</div><span class="text-xs text-gray-500">#{{ rule.account_id }}</span></td>
                <td class="p-2"><input :value="rule.priority ?? ''" type="number" step="1" class="input w-28" :aria-label="t('admin.scheduling.priority')" :placeholder="String(accountsByID.get(rule.account_id)?.priority ?? '')" @input="setPriority(rule, $event)"><div class="mt-1 text-xs text-gray-500">{{ t('admin.scheduling.inheritPriority') }}</div></td>
                <td class="p-2"><input v-model.number="rule.traffic_weight" type="number" min="0" step="1" class="input w-24" :aria-label="t('admin.scheduling.weight')"><span v-if="rule.traffic_weight === 0" class="mt-1 block text-xs text-amber-600">{{ t('admin.scheduling.zeroWeight') }}</span></td>
                <td class="p-2">{{ accountsByID.get(rule.account_id)?.concurrency ?? '—' }}<div class="text-xs text-gray-500">{{ t('admin.scheduling.editConcurrencyHint') }}</div></td>
                <td v-if="policy.mode === 'fill_first'" class="p-2"><input v-model.number="rule.fill_order" type="number" step="1" class="input w-24" :aria-label="t('admin.scheduling.fillOrder')"></td>
                <td class="p-2"><button type="button" class="btn btn-secondary whitespace-nowrap" @click="openControl(rule.account_id)">{{ t('admin.scheduling.controlTitle') }}</button><div v-if="controls[rule.account_id]" class="mt-1 text-xs">{{ t('admin.scheduling.states.' + controls[rule.account_id].state) }} · {{ controls[rule.account_id].active_attempts }}</div></td>
                <td class="p-2"><button type="button" class="text-red-600" :aria-label="t('common.delete') + ' ' + accountName(rule.account_id)" @click="policy.accounts.splice(index, 1)">{{ t('common.delete') }}</button></td>
              </tr></tbody>
            </table>
          </div>
          <p class="mt-3 text-xs text-gray-500">{{ t('admin.scheduling.ratioHint') }}</p>
        </section>

        <section class="scheduling-card">
          <div class="flex flex-wrap justify-between gap-2"><h2 class="font-semibold">{{ t('admin.scheduling.latencyProfiles') }}</h2><button type="button" class="btn btn-secondary" data-testid="add-profile" @click="addProfile">{{ t('admin.scheduling.addProfile') }}</button></div>
          <p class="mt-2 text-sm text-gray-500">{{ t('admin.scheduling.latencyHint') }}</p>
          <p class="mt-2 text-xs text-gray-500" data-testid="protocol-sampling-hint">{{ t('admin.scheduling.protocolSamplingHint') }}</p>
          <p v-if="!policy.profiles.length && !fallbackProfiles.length" class="mt-3 rounded-lg bg-blue-50 p-3 text-sm text-blue-700 dark:bg-blue-950/30 dark:text-blue-300" data-testid="observe-only">{{ t('admin.scheduling.observeOnly') }}</p>
          <div v-if="fallbackProfiles.length" class="mt-3 rounded-lg bg-blue-50 p-3 text-sm text-blue-700 dark:bg-blue-950/30 dark:text-blue-300" data-testid="inherited-profiles">
            <p>{{ t('admin.scheduling.inheritedProfiles', { version: globalDefaults?.version }) }}</p>
            <div class="mt-2 overflow-x-auto"><table class="w-full text-left text-xs"><thead><tr><th class="py-1 pr-3">{{ t('admin.scheduling.profileName') }}</th><th class="py-1 pr-3">{{ t('admin.scheduling.protocol') }}</th><th class="py-1 pr-3">{{ t('admin.scheduling.reasoning') }}</th><th v-for="key in latencyKeys" :key="key" class="py-1 pr-3">{{ t('admin.scheduling.latency.' + key) }}</th></tr></thead><tbody><tr v-for="profile in fallbackProfiles" :key="profile.name"><td class="py-1 pr-3">{{ profile.name }}</td><td class="py-1 pr-3">{{ profile.transport ? protocolLabel(profile.transport) : t('admin.scheduling.anyProtocol') }}</td><td class="py-1 pr-3">{{ profile.reasoning || t('admin.scheduling.anyReasoning') }}</td><td v-for="key in latencyKeys" :key="key" class="py-1 pr-3">{{ profile[key] / 1000 }}</td></tr></tbody></table></div>
          </div>
          <fieldset v-for="(profile, index) in policy.profiles" :key="index" class="mt-4 rounded-xl border border-gray-200 p-4 dark:border-dark-600">
            <legend class="px-2 text-sm font-medium">{{ profile.name || t('admin.scheduling.profile') }}</legend>
            <div class="grid gap-3 sm:grid-cols-3">
              <label class="scheduling-label">{{ t('admin.scheduling.profileName') }}<input v-model.trim="profile.name" class="input mt-1"></label>
              <label class="scheduling-label">{{ t('admin.scheduling.reasoning') }}<input v-model.trim="profile.reasoning" class="input mt-1" :placeholder="t('admin.scheduling.anyReasoning')"></label>
              <label class="scheduling-label">{{ t('admin.scheduling.protocol') }}<select v-model="profile.transport" class="input mt-1" :data-testid="'profile-' + index + '-transport'"><option value="">{{ t('admin.scheduling.anyProtocol') }}</option><option v-for="protocol in protocols" :key="protocol" :value="protocol">{{ protocolLabel(protocol) }}</option></select></label>
              <label class="scheduling-label">{{ t('admin.scheduling.contextMin') }}<input v-model.number="profile.context_min_tokens" type="number" min="0" step="1" class="input mt-1"></label>
              <label class="scheduling-label">{{ t('admin.scheduling.contextMax') }}<input v-model.number="profile.context_max_tokens" type="number" min="0" step="1" class="input mt-1"></label>
            </div>
            <div class="mt-4 grid gap-3 sm:grid-cols-5"><label v-for="key in latencyKeys" :key="key" class="scheduling-label">{{ t('admin.scheduling.latency.' + key) }}<input :value="profile[key] / 1000" type="number" min="0" step="0.001" class="input mt-1" :data-testid="'profile-' + index + '-' + key" @input="profile[key] = milliseconds($event)"></label></div>
            <div class="mt-3 flex flex-wrap justify-between gap-2"><button type="button" class="text-sm text-primary-600" @click="deriveRecovery(profile)">{{ t('admin.scheduling.deriveRecovery') }}</button><button type="button" class="text-sm text-red-600" @click="policy.profiles.splice(index, 1)">{{ t('common.delete') }}</button></div>
          </fieldset>
          <p class="mt-3 text-xs text-gray-500">{{ t('admin.scheduling.healthDefaults') }}</p>
        </section>

        <section class="scheduling-card">
          <h2 class="font-semibold">{{ t('admin.scheduling.retry') }}</h2><p class="mt-2 text-sm text-gray-500">{{ t('admin.scheduling.retryHint') }}</p>
          <div class="mt-4 grid gap-3 sm:grid-cols-3"><label v-for="key in retryNumberKeys" :key="key" class="scheduling-label">{{ t('admin.scheduling.retryFields.' + key) }}<input v-model.number="policy.retry[key]" type="number" :min="key === 'max_after_timeout' ? 0 : 1" step="1" class="input mt-1" :data-testid="'retry-' + key"></label></div>
          <div class="mt-4 grid gap-3 sm:grid-cols-2"><label class="scheduling-label">{{ t('admin.scheduling.sameTierMode') }}<select v-model="policy.retry.mode" class="input mt-1"><option value="bounded_same_tier_first">{{ t('admin.scheduling.boundedSameTier') }}</option><option value="exhaust_same_tier">{{ t('admin.scheduling.exhaustSameTier') }}</option></select></label><label class="scheduling-label">{{ t('admin.scheduling.switchMargin') }}<input v-model.number="policy.retry.switch_margin_ms" type="number" min="0" step="1" class="input mt-1"></label></div>
          <div class="mt-4 flex flex-wrap gap-5 text-sm"><label class="flex items-center gap-2"><input v-model="policy.retry.cross_tier" type="checkbox">{{ t('admin.scheduling.crossTier') }}</label><label class="flex items-center gap-2"><input v-model="policy.retry.reserve_fallback" type="checkbox">{{ t('admin.scheduling.reserveFallback') }}</label></div>
          <p v-if="policy.retry.mode === 'exhaust_same_tier'" class="mt-3 text-sm text-amber-700">{{ t('admin.scheduling.exhaustWarning') }}</p>
        </section>
        <div class="sticky bottom-3 z-10 flex flex-wrap items-center justify-between gap-3 rounded-xl border border-gray-200 bg-white/95 p-4 shadow-lg dark:border-dark-600 dark:bg-dark-800/95">
          <span class="text-sm" :class="conflict || !scopeMatches ? 'text-amber-600' : 'text-gray-500'">{{ conflict ? t('admin.scheduling.policyConflict') : !scopeMatches ? t('admin.scheduling.scopeChanged') : t('admin.scheduling.saveHint') }}</span>
          <button type="submit" class="btn btn-primary" :disabled="saving || loading || conflict || !scopeMatches" data-testid="save-policy">{{ saving ? t('common.loading') : t('common.save') }}</button>
        </div>
        </fieldset>
      </form>

      <section v-if="policy" class="scheduling-card">
        <h2 class="font-semibold">{{ t('admin.scheduling.explainTitle') }}</h2><p class="mt-2 text-sm text-gray-500">{{ t('admin.scheduling.explainHint') }}</p>
        <div class="mt-4 grid gap-3 sm:grid-cols-3"><label class="scheduling-label">{{ t('admin.scheduling.protocol') }}<select v-model="explainProtocol" class="input mt-1" data-testid="explain-protocol"><option v-for="protocol in protocols" :key="protocol" :value="protocol">{{ protocolLabel(protocol) }}</option></select></label><label class="scheduling-label">{{ t('admin.scheduling.reasoning') }}<input v-model.trim="explainReasoning" class="input mt-1"></label><label class="scheduling-label">{{ t('admin.scheduling.contextTokens') }}<input v-model.number="explainContext" type="number" min="0" step="1" class="input mt-1" :placeholder="t('admin.scheduling.unknownContext')"></label></div>
        <button type="button" class="btn btn-secondary mt-4" :disabled="explaining || saving || !scopeMatches" data-testid="explain" @click="runExplain">{{ explaining ? t('common.loading') : t('admin.scheduling.runExplain') }}</button>
        <div v-if="explanation" class="mt-4" aria-live="polite"><p class="text-sm font-medium">{{ t('admin.scheduling.explainResult', { version: explanation.policy_version }) }} · {{ explanation.reason }}</p><p class="mt-1 text-sm">{{ t('admin.scheduling.selectedAccount') }}: {{ explanation.selected_account_id ? accountName(explanation.selected_account_id) : '—' }}</p>
          <div class="mt-3 overflow-x-auto"><table class="w-full text-left text-sm"><thead><tr><th class="p-2">{{ t('admin.scheduling.account') }}</th><th class="p-2">{{ t('admin.scheduling.priority') }}</th><th class="p-2">{{ t('admin.scheduling.weight') }}</th><th class="p-2">{{ t('admin.scheduling.targetShare') }}</th><th class="p-2">{{ t('admin.scheduling.health') }}</th><th class="p-2">{{ t('admin.scheduling.recoveryProgress') }}</th><th class="p-2">{{ t('admin.scheduling.decision') }}</th></tr></thead><tbody><tr v-for="candidate in explanation.candidates" :key="candidate.account_id" class="border-t border-gray-100 dark:border-dark-700"><td class="p-2">{{ candidate.account_name || accountName(candidate.account_id) }}</td><td class="p-2">{{ candidate.priority }}</td><td class="p-2">{{ candidate.traffic_weight }}</td><td class="p-2">{{ candidate.target_share == null ? '—' : (candidate.target_share * 100).toFixed(1) + '%' }}</td><td class="p-2">{{ candidate.health || '—' }} / {{ candidate.control_state || '—' }}<div v-if="candidate.current_concurrency != null" class="text-xs text-gray-500">{{ candidate.current_concurrency }} / {{ candidate.concurrency_limit ?? '—' }}</div></td><td class="p-2"><span>{{ recoveryStage(candidate) }}</span><div v-if="candidate.health?.toLowerCase() === 'recovering'" class="text-xs text-gray-500">{{ t('admin.scheduling.recoveryGood') }} {{ candidate.good_streak ?? t('admin.scheduling.unobserved') }}<br>{{ t('admin.scheduling.effectiveShare') }}: {{ candidate.effective_share == null ? t('admin.scheduling.unobserved') : (candidate.effective_share * 100).toFixed(2) + '%' }}</div></td><td class="p-2" :class="candidate.eligible ? 'text-emerald-600' : 'text-amber-600'">{{ candidate.reason }}</td></tr></tbody></table></div>
          <p class="mt-2 text-xs text-gray-500">{{ t('admin.scheduling.noActualMetrics') }}</p>
        </div>
      </section>
      <SchedulingTrafficStats v-if="policy" :group-id="policy.group_id" :model="policy.model" :candidates="explanation?.candidates" />
      <SchedulingRequestAttempts :initial-request-id="typeof route.query.request_id === 'string' ? route.query.request_id : ''" />
      <AccountSchedulingControlDialog :show="controlAccount !== null" :account="controlAccount" @close="controlAccount = null" @updated="onControlUpdated" @observed="onControlUpdated" />
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import AccountSchedulingControlDialog from '@/components/account/AccountSchedulingControlDialog.vue'
import SchedulingRequestAttempts from '@/components/account/SchedulingRequestAttempts.vue'
import SchedulingTrafficStats from '@/components/account/SchedulingTrafficStats.vue'
import schedulingAPI from '@/api/admin/scheduling'
import accountsAPI from '@/api/admin/accounts'
import groupsAPI from '@/api/admin/groups'
import type { AccountListItem, AdminGroup } from '@/types'
import type { AccountSchedulingControl, AccountSchedulingRule, ModelLatencyProfile, SchedulingExplainCandidate, SchedulingExplanation, SchedulingPolicy } from '@/types/scheduling'
import { createLatencyProfile, createSchedulingPolicy, isSchedulingConflict, latencyKeys, validateSchedulingPolicy } from '@/utils/scheduling'
import { extractApiErrorMessage } from '@/utils/apiError'

const { t } = useI18n()
const route = useRoute()
const scopeGroup = ref(0)
const scopeModel = ref(typeof route.query.model === 'string' ? route.query.model : '')
const policy = ref<SchedulingPolicy | null>(null)
const loadedVersion = ref(0)
const inheritedPolicyVersion = ref<number | null>(null)
const globalDefaults = ref<SchedulingPolicy | null>(null)
const groups = ref<AdminGroup[]>([])
const accounts = ref<AccountListItem[]>([])
const loading = ref(false)
const saving = ref(false)
const explaining = ref(false)
const conflict = ref(false)
const error = ref('')
const notice = ref('')
const addAccountID = ref(0)
const controlAccount = ref<{ id: number; name: string } | null>(null)
const controls = ref<Record<number, AccountSchedulingControl>>({})
const explanation = ref<SchedulingExplanation | null>(null)
const explainProtocol = ref('responses')
const explainReasoning = ref('')
const explainContext = ref<number | ''>('')
const modes = ['swrr', 'pin', 'fill_first'] as const
const degradedModes = ['bounded_best_effort', 'strict_priority', 'fail_fast'] as const
const protocols = ['responses', 'chat', 'messages', 'gemini', 'ws', 'http'] as const
const retryNumberKeys = ['max_attempts', 'max_per_tier', 'max_per_account', 'max_after_timeout', 'initial_per_token', 'burst'] as const
const accountsByID = computed(() => new Map(accounts.value.map(account => [account.id, account])))
const scopedAccounts = computed(() => accounts.value.filter(account => !policy.value?.group_id || account.group_ids?.includes(policy.value.group_id)))
const availableAccounts = computed(() => scopedAccounts.value.filter(account => !policy.value?.accounts.some(rule => rule.account_id === account.id)))
const scopeMatches = computed(() => policy.value?.group_id === scopeGroup.value && policy.value?.model === scopeModel.value.trim())
const fallbackProfiles = computed(() => inheritedPolicyVersion.value === null && policy.value?.group_id ? globalDefaults.value?.profiles ?? [] : [])
const requests = new AbortController()
let loadGeneration = 0
let alive = true
function protocolLabel(protocol: string) { return t('admin.scheduling.protocolOptions.' + protocol) }
function normalizeProfileProtocols(value: SchedulingPolicy | null) { for (const profile of value?.profiles ?? []) { if (profile.transport === 'anthropic') profile.transport = 'messages' } }
function recoveryStage(candidate: SchedulingExplainCandidate): string { if (candidate.health?.toLowerCase() !== 'recovering') return '—'; return candidate.recovery_stage === 0 ? '10%' : candidate.recovery_stage === 1 ? '30%' : t('admin.scheduling.unobserved') }
function milliseconds(event: Event): number { return Math.round(Number((event.target as HTMLInputElement).value) * 1000) }
function accountName(id: number): string { return accountsByID.value.get(id)?.name || '#' + id }
function setPriority(rule: AccountSchedulingRule, event: Event) { const value = (event.target as HTMLInputElement).value; rule.priority = value === '' ? null : Number(value) }
function addAccount() { if (!policy.value || !addAccountID.value) return; policy.value.accounts.push({ account_id: addAccountID.value, priority: null, traffic_weight: 1, fill_order: policy.value.accounts.length }); addAccountID.value = 0 }
function addProfile() { if (!policy.value) return; const profile = createLatencyProfile(); profile.name = 'profile-' + (policy.value.profiles.length + 1); policy.value.profiles.push(profile) }
function deriveRecovery(profile: ModelLatencyProfile) { profile.recovery_threshold_ms = Math.round(profile.health_threshold_ms * 0.75); profile.min_attempt_window_ms = profile.health_threshold_ms }
function openControl(id: number) { controlAccount.value = { id, name: accountName(id) } }
function onControlUpdated(control: AccountSchedulingControl) { controls.value[control.account_id] = control }
async function loadPolicy() {
  if (!scopeModel.value.trim() || saving.value) return
  const generation = ++loadGeneration
  loading.value = true; error.value = ''; notice.value = ''; explanation.value = null
  const groupID = scopeGroup.value; const model = scopeModel.value.trim()
  try {
    const [document, defaults] = await Promise.all([
      schedulingAPI.getPolicy(groupID, model, requests.signal),
      groupID ? schedulingAPI.getPolicy(0, model, requests.signal) : Promise.resolve(null)
    ])
    if (!alive || generation !== loadGeneration) return
    globalDefaults.value = defaults?.policy ? structuredClone(defaults.policy) : null
    inheritedPolicyVersion.value = !document.policy && defaults?.policy ? defaults.version : null
    const effective = document.policy ?? defaults?.policy
    policy.value = effective ? { ...structuredClone(effective), group_id: groupID, model, version: document.version } : createSchedulingPolicy(groupID, model)
    policy.value.accounts ??= []; policy.value.profiles ??= []
    normalizeProfileProtocols(policy.value); normalizeProfileProtocols(globalDefaults.value)
    loadedVersion.value = document.version; conflict.value = false
  } catch (err) { if (alive && generation === loadGeneration) error.value = extractApiErrorMessage(err, t('admin.scheduling.loadFailed')) }
  finally { if (alive && generation === loadGeneration) loading.value = false }
}
async function save() {
  if (!policy.value || !scopeMatches.value || saving.value || conflict.value) return
  error.value = ''; notice.value = ''
  const validation = validateSchedulingPolicy(policy.value)
  if (validation) { error.value = t('admin.scheduling.errors.' + validation); return }
  saving.value = true
  try {
    const document = await schedulingAPI.savePolicy(JSON.parse(JSON.stringify(policy.value)), loadedVersion.value)
    if (!alive) return
    policy.value = document.policy; loadedVersion.value = document.version; explanation.value = null; inheritedPolicyVersion.value = null
    notice.value = t('admin.scheduling.saved', { version: document.version })
  } catch (err) {
    if (!alive) return
    conflict.value = isSchedulingConflict(err)
    error.value = conflict.value ? t('admin.scheduling.policyConflict') : extractApiErrorMessage(err, t('admin.scheduling.saveFailed'))
  } finally { saving.value = false }
}
async function runExplain() {
  if (!policy.value || !scopeMatches.value || explaining.value) return
  if (explainContext.value !== '' && (!Number.isSafeInteger(explainContext.value) || explainContext.value < 0)) { error.value = t('admin.scheduling.errors.invalidContext'); return }
  explaining.value = true; error.value = ''; explanation.value = null
  const generation = loadGeneration
  try {
    const result = await schedulingAPI.explain({ group_id: policy.value.group_id, model: policy.value.model, protocol: explainProtocol.value,
      reasoning_effort: explainReasoning.value, ...(explainContext.value === '' ? {} : { context_tokens: explainContext.value }) }, requests.signal)
    if (alive && generation === loadGeneration) explanation.value = result
  } catch (err) { if (alive) error.value = extractApiErrorMessage(err, t('admin.scheduling.explainFailed')) }
  finally { explaining.value = false }
}
onMounted(async () => {
  const groupPromise = groupsAPI.getAllIncludingInactive().then(result => { if (alive) groups.value = result })
  const accountPromise = (async () => {
    const collected: AccountListItem[] = []
    for (let page = 1; alive; page++) {
      const result = await accountsAPI.list(page, 100, { lite: 'true' }, { signal: requests.signal })
      collected.push(...result.items)
      if (!result.items.length || collected.length >= result.total) break
    }
    if (alive) accounts.value = collected
  })()
  const results = await Promise.allSettled([groupPromise, accountPromise])
  if (!alive) return
  const failed = results.find(result => result.status === 'rejected')
  if (failed?.status === 'rejected') error.value = extractApiErrorMessage(failed.reason, t('admin.scheduling.loadFailed'))
  const accountID = Number(route.query.account)
  if (Number.isSafeInteger(accountID) && accountID > 0) openControl(accountID)
  if (scopeModel.value) await loadPolicy()
})
onUnmounted(() => { alive = false; loadGeneration++; requests.abort() })
</script>

<style scoped>
.scheduling-card { @apply rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-800 sm:p-5; }
.scheduling-label { @apply text-sm font-medium text-gray-700 dark:text-dark-200; }
</style>
