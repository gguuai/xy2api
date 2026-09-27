<template>
  <BaseDialog :show="show" :title="t('admin.scheduling.controlTitle') + ' · ' + (account?.name || '')" width="wide" @close="emit('close')">
    <div class="space-y-4">
      <p class="text-sm text-gray-500 dark:text-dark-400">{{ t('admin.scheduling.drainHint') }}</p>
      <label class="block text-sm font-medium">
        {{ t('admin.scheduling.scope') }}
        <select v-model="scope" class="input mt-1" :disabled="busy" data-testid="control-scope" @change="refresh(true)">
          <option value="logical_account">{{ t('admin.scheduling.logicalAccount') }}</option>
          <option value="credential_family">{{ t('admin.scheduling.credentialFamily') }}</option>
        </select>
      </label>
      <p v-if="scope === 'credential_family'" class="text-sm text-amber-700 dark:text-amber-300">{{ t('admin.scheduling.familyWarning') }}</p>
      <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
      <div v-if="control" class="rounded-xl border border-gray-200 p-4 dark:border-dark-600" aria-live="polite">
        <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
          <strong :class="control.state === 'RUNNING' ? 'text-emerald-600' : 'text-amber-600'" data-testid="control-state">{{ t('admin.scheduling.states.' + control.state) }}</strong>
          <span class="text-xs text-gray-500">{{ t('admin.scheduling.epoch', { value: control.epoch }) }}</span>
        </div>
        <dl class="grid grid-cols-2 gap-3 text-sm">
          <div v-for="key in countKeys" :key="key"><dt class="text-gray-500">{{ t('admin.scheduling.' + key) }}</dt><dd class="font-semibold">{{ control[key] }}</dd></div>
        </dl>
        <p v-if="control.session_deadline" class="mt-3 text-sm">{{ t('admin.scheduling.sessionDeadline') }} {{ formatDateTime(control.session_deadline) }}</p>
        <p v-if="control.state === 'DRAIN_UNCERTAIN'" class="mt-3 text-sm text-amber-700">{{ t('admin.scheduling.uncertainHint') }}</p>
        <p v-if="drainOverdue" class="mt-3 text-sm text-amber-700" role="status" data-testid="drain-warning">{{ t('admin.scheduling.drainOverdue') }}</p>
        <p v-if="control.notification_warning" class="mt-3 text-sm text-amber-700">{{ control.notification_warning }}</p>
      </div>
      <label class="block text-sm font-medium">
        {{ t('admin.scheduling.action') }}
        <select v-model="action" class="input mt-1" :disabled="busy" data-testid="control-action" @change="forceConfirmed = false">
          <option value="pause">{{ t('admin.scheduling.pause') }}</option>
          <option value="session_drain">{{ t('admin.scheduling.sessionDrain') }}</option>
          <option value="resume">{{ t('admin.scheduling.resume') }}</option>
          <option value="force_stop">{{ t('admin.scheduling.forceStop') }}</option>
        </select>
      </label>
      <div v-if="action === 'session_drain'" class="grid grid-cols-2 gap-3">
        <label class="text-sm">{{ t('admin.scheduling.sessionSeconds') }}<input v-model.number="sessionSeconds" type="number" min="1" max="3600" step="1" class="input mt-1" data-testid="session-seconds"></label>
        <label class="text-sm">{{ t('admin.scheduling.sessionTurns') }}<input v-model.number="sessionTurns" type="number" min="1" max="100" step="1" class="input mt-1" data-testid="session-turns"></label>
      </div>
      <p v-if="action === 'resume'" class="text-sm text-gray-500">{{ t('admin.scheduling.resumeHint') }}</p>
      <label v-if="action === 'force_stop'" class="flex items-start gap-2 rounded-xl bg-red-50 p-3 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-300">
        <input v-model="forceConfirmed" type="checkbox" class="mt-1" data-testid="force-confirmed">{{ t('admin.scheduling.forceWarning') }}
      </label>
    </div>
    <template #footer>
      <button class="btn btn-secondary" :disabled="busy || loading" @click="refresh(true)">{{ t('common.refresh') }}</button>
      <button class="btn" :class="action === 'force_stop' ? 'btn-danger' : 'btn-primary'" :disabled="!canSubmit" data-testid="apply-control" @click="applyControl">{{ busy ? t('common.loading') : t('admin.scheduling.applyControl') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import schedulingAPI from '@/api/admin/scheduling'
import { extractApiErrorMessage } from '@/utils/apiError'
import { isSchedulingConflict } from '@/utils/scheduling'
import { formatDateTime } from '@/utils/format'
import type { AccountSchedulingControl, SchedulingControlCommand, SchedulingControlScope } from '@/types/scheduling'

const props = defineProps<{ show: boolean; account: { id: number; name: string } | null }>()
const emit = defineEmits<{ close: []; updated: [control: AccountSchedulingControl]; observed: [control: AccountSchedulingControl] }>()
const { t } = useI18n()
const scope = ref<SchedulingControlScope>('logical_account')
const action = ref<SchedulingControlCommand['action']>('pause')
const control = ref<AccountSchedulingControl | null>(null)
const busy = ref(false)
const loading = ref(false)
const error = ref('')
const forceConfirmed = ref(false)
const sessionSeconds = ref(300)
const sessionTurns = ref(3)
const countKeys = ['active_attempts', 'unknown_attempts', 'pending_settlements', 'allowed_sessions'] as const
const drainOverdue = computed(() => {
  const current = control.value
  if (!current || !['DRAINING', 'DRAIN_UNCERTAIN'].includes(current.state)) return false
  const updated = Date.parse(current.updated_at)
  return Number.isFinite(updated) && Date.now() - updated >= 5 * 60 * 1000
})
let generation = 0
let timer: ReturnType<typeof setTimeout> | undefined
let controller: AbortController | undefined
const canSubmit = computed(() => control.value && !busy.value && !loading.value && (action.value !== 'force_stop' || forceConfirmed.value) &&
  (action.value !== 'session_drain' || (Number.isInteger(sessionSeconds.value) && sessionSeconds.value >= 1 && sessionSeconds.value <= 3600 && Number.isInteger(sessionTurns.value) && sessionTurns.value >= 1 && sessionTurns.value <= 100)))
function stopPolling() { clearTimeout(timer); controller?.abort(); generation++ }
async function refresh(reset = false) {
  if (!props.show || !props.account || busy.value) return
  clearTimeout(timer)
  controller?.abort()
  controller = new AbortController()
  const requestGeneration = ++generation
  const id = props.account.id
  const requestedScope = scope.value
  if (reset) control.value = null
  loading.value = true
  try {
    const result = await schedulingAPI.getControl(id, requestedScope, controller.signal)
    if (requestGeneration !== generation) return
    control.value = result
    error.value = ''
    emit('observed', result)
  } catch (err) {
    if (requestGeneration !== generation) return
    control.value = null // Never perform a mutation against a stale unreadable control state.
    error.value = extractApiErrorMessage(err, t('admin.scheduling.loadFailed'))
  } finally {
    if (requestGeneration === generation) {
      loading.value = false
      if (props.show) timer = setTimeout(() => refresh(), 5000)
    }
  }
}
async function applyControl() {
  if (!canSubmit.value || !control.value || !props.account) return
  stopPolling()
  busy.value = true
  error.value = ''
  const id = props.account.id
  const requestGeneration = generation
  const command: SchedulingControlCommand = { action: action.value, scope: scope.value, expected_epoch: control.value.epoch }
  if (action.value === 'session_drain') { command.session_duration_seconds = sessionSeconds.value; command.session_max_turns = sessionTurns.value }
  try {
    const result = await schedulingAPI.setControl(id, command)
    if (requestGeneration !== generation) return
    control.value = result
    forceConfirmed.value = false
    emit('updated', result)
  } catch (err) {
    if (requestGeneration !== generation) return
    error.value = isSchedulingConflict(err) ? t('admin.scheduling.controlConflict') : extractApiErrorMessage(err, t('admin.scheduling.saveFailed'))
    if (isSchedulingConflict(err)) control.value = null
  } finally {
    busy.value = false
    if (requestGeneration === generation && props.show && control.value) timer = setTimeout(() => refresh(), 5000)
    else if (requestGeneration !== generation && props.show && props.account) void refresh(true)
  }
}
watch(() => [props.show, props.account?.id], () => {
  stopPolling(); control.value = null; error.value = ''; forceConfirmed.value = false
  scope.value = 'logical_account'; action.value = 'pause'
  if (props.show && props.account) void refresh(true)
}, { immediate: true })
onUnmounted(stopPolling)
</script>
