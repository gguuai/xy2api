<template>
  <BaseDialog :show="show" :title="t('admin.accounts.iqRecords')" width="wide" @close="$emit('close')">
    <div class="mb-5 flex flex-wrap items-center justify-between gap-3">
      <div class="min-w-0">
        <p class="break-words text-sm font-medium text-gray-900 dark:text-gray-100">{{ account?.name }}</p>
        <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqHistoryRetention') }}</p>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <button type="button" class="btn btn-primary inline-flex items-center gap-2" :disabled="!state?.enabled || queuing || state.execution_state === 'running' || state.execution_state === 'pending' || state.execution_state === 'retry_wait'" @click="queueCheck">
          <Icon name="play" size="sm" />{{ t('admin.accounts.iqRun') }}
        </button>
        <button type="button" class="btn btn-secondary inline-flex items-center gap-2" :disabled="downloading || !account || !records.length" @click="downloadDiagnostics">
          <Icon name="download" size="sm" />{{ t('admin.accounts.iqDownload') }}
        </button>
        <button type="button" class="btn btn-secondary h-10 w-10 p-0" :disabled="loading || !account" :title="t('common.refresh')" :aria-label="t('common.refresh')" @click="loadRecords">
          <Icon name="refresh" size="sm" :class="loading && 'animate-spin'" />
        </button>
      </div>
    </div>
    <p v-if="copyMessage" role="status" class="mb-3 text-sm">{{ copyMessage }}</p>
    <p v-if="queueError" role="alert" class="mb-3 text-sm text-red-600 dark:text-red-400">{{ queueError }}</p>
    <p v-if="queueMessage" role="status" class="mb-3 text-sm text-gray-700 dark:text-gray-300">{{ queueMessage }}</p>
    <div v-if="state" class="mb-5 border-b border-gray-200 pb-5 dark:border-dark-600" data-testid="iq-current-state" :aria-label="t('admin.accounts.iqCurrentState')">
      <p class="text-sm font-medium text-gray-900 dark:text-gray-100">{{ state.enabled ? t('admin.accounts.iqCheckStatus.' + (state.last_run_status === 'unknown' ? 'unknown' : state.status || 'unknown')) : t('admin.accounts.iqOff') }}</p>
      <p v-if="state.enabled && state.last_run_status === 'unknown' && state.last_valid_at" class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqPreviousValid') }}<span v-if="state.last_valid_status"> · {{ t('admin.accounts.iqCheckStatus.' + state.last_valid_status) }}</span> · {{ formatDateTime(state.last_valid_at) }}</p>
      <p v-if="state.scheduling_blocked" class="mt-2 text-sm text-amber-700 dark:text-amber-300">{{ t('admin.accounts.iqTrafficAvoidance') }}</p>
      <p v-if="currentReason" class="mt-2 text-sm text-gray-700 dark:text-gray-300">{{ currentReason }}</p>
      <dl class="mt-4 grid grid-cols-1 gap-3 text-xs sm:grid-cols-2">
        <div><dt class="text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqLastAssessment') }}</dt><dd class="mt-1 tabular-nums">{{ state.last_run_at ? formatDateTime(state.last_run_at) : t('admin.accounts.iqNotChecked') }}</dd></div>
        <div><dt class="text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqNextCheck') }}</dt><dd class="mt-1 tabular-nums">{{ iqNextLabel(state, t, formatDateTime) }}</dd></div>
      </dl>
    </div>
    <p v-if="statusError" role="alert" class="mb-3 text-sm text-red-600 dark:text-red-400">{{ statusError }}</p>
    <p v-if="downloadError" role="alert" class="mb-3 text-sm text-red-600 dark:text-red-400">{{ downloadError }}</p>
    <div v-if="error" role="alert" class="py-4 text-center">
      <p class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
      <button type="button" class="btn btn-secondary mt-3" @click="loadRecords">{{ t('common.refresh') }}</button>
    </div>
    <div v-if="loading && !records.length" role="status" class="flex items-center justify-center gap-2 py-12 text-sm text-gray-500 dark:text-dark-400"><Icon name="refresh" size="sm" class="animate-spin" />{{ t('common.loading') }}</div>
    <div v-else-if="!records.length && !error" class="py-12 text-center text-gray-500 dark:text-dark-400"><Icon name="clock" class="mx-auto mb-3" /><p class="text-sm">{{ t('admin.accounts.iqEmpty') }}</p></div>
    <ol v-if="records.length" class="divide-y divide-gray-200 dark:divide-dark-600">
      <li v-for="record in records" :key="record.id" class="py-5 first:pt-0 last:pb-0">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <span class="inline-flex items-center rounded-md px-2 py-1 text-xs font-medium" :class="statusClass(record)">{{ record.finished_at ? t('admin.accounts.iqCheckStatus.' + record.status) : t(state?.execution_state === 'retry_wait' ? 'admin.accounts.iqExecutionStates.retry_wait' : 'admin.accounts.iqRunning') }}</span>
          <time :datetime="record.started_at" class="text-xs tabular-nums text-gray-500 dark:text-dark-400">{{ formatDateTime(record.started_at) }}</time>
        </div>
        <p v-if="record.reason && record.reason !== 'correct_answer'" class="mt-3 break-words text-sm text-gray-700 dark:text-gray-300">{{ reasonLabel(record.reason) }}</p>
        <dl class="mt-4 grid grid-cols-2 gap-x-5 gap-y-4 sm:grid-cols-4">
          <div class="min-w-0"><dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqNormalizedAnswer') }}</dt><dd class="mt-1 break-words text-base font-semibold tabular-nums" data-testid="iq-answer">{{ record.normalized_answer ?? '—' }}</dd></div>
          <div class="min-w-0"><dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqModel') }}</dt><dd class="mt-1 break-all text-sm text-gray-900 dark:text-gray-100">{{ record.model || '—' }}</dd></div>
          <div class="min-w-0"><dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqEffort') }}</dt><dd class="mt-1 break-words text-sm text-gray-900 dark:text-gray-100">{{ iqEffortLabel(record.effort) }}</dd></div>
          <div class="min-w-0"><dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqDuration') }}</dt><dd class="mt-1 text-sm tabular-nums text-gray-900 dark:text-gray-100">{{ record.finished_at ? (record.latency_ms / 1000).toFixed(2) + ' s' : '—' }}</dd></div>
        </dl>
        <details v-if="record.answer" class="mt-4">
          <summary class="cursor-pointer text-sm text-gray-600 dark:text-gray-300">{{ t('admin.accounts.iqRawAnswer') }}</summary>
          <pre class="mt-2 max-h-64 overflow-auto whitespace-pre-wrap break-words rounded-lg bg-gray-50 p-3 text-sm text-gray-800 dark:bg-dark-700 dark:text-gray-200">{{ record.answer || (record.reason ? reasonLabel(record.reason) : t('admin.accounts.iqRunning')) }}</pre>
        </details>
        <details v-if="record.attempts && record.attempts.length > 1" class="mt-3" data-testid="iq-attempts">
          <summary class="cursor-pointer text-sm text-gray-600 dark:text-gray-300">{{ t('admin.accounts.iqAttempts', { count: record.attempts.length }) }}</summary>
          <ol class="mt-2 divide-y divide-gray-100 dark:divide-dark-700">
            <li v-for="attempt in record.attempts" :key="attempt.attempt_no" class="flex flex-wrap justify-between gap-2 py-2 text-sm">
              <span>{{ t('admin.accounts.iqAttemptNumber', { number: attempt.attempt_no }) }} · {{ attempt.finished_at ? reasonLabel(attempt.reason) : t('admin.accounts.iqRunning') }}</span>
              <span class="tabular-nums">{{ attempt.finished_at ? (attempt.latency_ms / 1000).toFixed(2) + ' s' : '—' }}</span>
                  </li>
          </ol>
        </details>
        <details class="mt-3">
          <summary class="cursor-pointer text-sm text-gray-600 dark:text-gray-300">{{ t('admin.accounts.iqDiagnostic') }}</summary>
          <p v-if="!record.grader_version || record.grader_version === 'candy-grader-v1'" class="input-hint">{{ t('admin.accounts.iqLegacyResult') }}</p>
          <p v-if="record.finished_at && record.format_compliant === false && record.normalized_answer != null" class="input-hint">{{ t('admin.accounts.iqFormatMismatch') }}</p>
          <dl class="mt-3 grid grid-cols-1 gap-x-5 gap-y-3 text-sm sm:grid-cols-2">
            <div v-for="row in iqDiagnosticSummary(record, t)" :key="row.label" class="min-w-0">
              <dt class="text-xs text-gray-500 dark:text-dark-400">{{ row.label }}</dt><dd class="mt-1 break-words tabular-nums">{{ row.value }}</dd>
            </div>
          </dl>
        </details>
        <details v-if="iqTechnicalGroups(record, t, te).length" class="mt-3" data-testid="iq-technical-details">
          <summary class="cursor-pointer text-sm text-gray-600 dark:text-gray-300">{{ t('admin.accounts.iqTechnicalDetails') }}</summary>
          <section v-for="group in iqTechnicalGroups(record, t, te)" :key="group.label" class="mt-4">
            <h4 class="mb-2 text-sm font-medium">{{ group.label }}</h4>
            <dl class="grid grid-cols-1 gap-x-5 gap-y-2 text-xs sm:grid-cols-2">
              <div v-for="row in group.rows" :key="row.label" class="min-w-0">
                <dt class="text-gray-500 dark:text-dark-400">{{ row.label }}</dt>
                <dd class="mt-1 break-words">{{ row.value }}<code v-if="row.raw && row.raw !== row.value" class="ml-2 break-all text-gray-500 dark:text-dark-400">{{ row.raw }}</code>
                  <button v-if="row.raw" type="button" class="ml-2 rounded p-1 text-gray-600 focus-visible:ring-2 focus-visible:ring-blue-500 dark:text-gray-300" :aria-label="t('admin.accounts.iqCopyField', { field: row.label })" @click="copyValue(row.raw)"><Icon name="copy" size="xs" /></button>
                </dd>
              </div>
            </dl>
          </section>
        </details>
      </li>
    </ol>
  </BaseDialog>
</template>
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { downloadIQCheckDiagnostics } from '@/api/admin/accounts'
import { adminAPI } from '@/api/admin'
import type { Account, IQCheckRecord } from '@/types'
import { formatDateTime } from '@/utils/format'
import { iqDiagnosticSummary, iqTechnicalGroups, iqEffortLabel, iqNextLabel } from './iqCheckPresentation'
const props = defineProps<{ show: boolean; account: Account | null }>()
const emit = defineEmits<{ close: []; updated: [state: NonNullable<Account['iq_check']>] }>()
const { t, te } = useI18n()
const reasonLabel = (reason: string) => te(`admin.accounts.iqReasons.${reason}`) ? t(`admin.accounts.iqReasons.${reason}`) : reason
const executionReasonLabel = (reason: string) => te(`admin.accounts.iqExecutionReasons.${reason}`) ? t(`admin.accounts.iqExecutionReasons.${reason}`) : reasonLabel(reason)
const statusClass = (record: IQCheckRecord) => !record.finished_at ? 'bg-blue-50 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300' : record.status === 'smart' ? 'bg-green-50 text-green-700 dark:bg-green-900/30 dark:text-green-300' : record.status === 'degraded' ? 'bg-amber-50 text-amber-800 dark:bg-amber-900/30 dark:text-amber-300' : 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
const downloading = ref(false)
const downloadError = ref('')
let downloadRequest = 0
async function downloadDiagnostics() {
 if (!props.account || downloading.value) return
 const version = ++downloadRequest
 downloading.value = true
 downloadError.value = ''
 try { const blob = await downloadIQCheckDiagnostics(props.account.id); if (version !== downloadRequest) return; const url = URL.createObjectURL(blob); const link = document.createElement('a'); link.href = url; link.download = 'iq-check-diagnostics.json'; document.body.appendChild(link); link.click(); link.remove(); setTimeout(() => URL.revokeObjectURL(url), 1000) }
 catch { if (version === downloadRequest) downloadError.value = t('admin.accounts.iqDownloadFailed') }
 finally { if (version === downloadRequest) downloading.value = false }
}
const records = ref<IQCheckRecord[]>([])
const loading = ref(false)
const error = ref('')
const state = ref<Account['iq_check']>()
const currentReason = computed(() => {
  const value = state.value
  if (!value?.enabled) return ''
  const reason = (value.last_run_status === 'unknown' ? value.last_run_reason : '') || value.execution_reason
  if (reason === 'minimum_interval') return ''
  if (reason === 'http_403' && value.execution_state !== 'paused') return t('admin.accounts.iqForbiddenRetry')
  return reason ? executionReasonLabel(reason) : ''
})
const copyMessage = ref('')
async function copyValue(value: string) {
  try { await navigator.clipboard.writeText(value); copyMessage.value = t('admin.accounts.iqCopied') }
  catch { copyMessage.value = t('admin.accounts.iqCopyFailed') }
}
const statusError = ref('')
const queuing = ref(false)
const queueError = ref('')
const queueMessage = ref('')
let queueRequest = 0
let request = 0
let controller: AbortController | undefined
let refreshTimer: ReturnType<typeof setTimeout> | undefined
function stopRefresh() { clearTimeout(refreshTimer); controller?.abort() }
function scheduleRefresh() {
  clearTimeout(refreshTimer)
  if (!props.show) return
  refreshTimer = setTimeout(() => {
    if (document.visibilityState === 'hidden') scheduleRefresh()
    else void loadRecords()
  }, state.value?.execution_state === 'running' || state.value?.execution_state === 'pending' || state.value?.execution_state === 'retry_wait' ? 5000 : 15000)
}
async function queueCheck() {
  const id = props.account?.id
  if (!id || !state.value?.enabled || queuing.value) return
  const version = ++queueRequest
  queuing.value = true
  queueError.value = ''; queueMessage.value = ''
  try {
    const result = await adminAPI.accounts.runIQCheck(id)
    if (version !== queueRequest) return
    queueMessage.value = result?.next_eligible_at ? t('admin.accounts.iqQueuedAt', { time: formatDateTime(result.next_eligible_at) }) : t('admin.accounts.iqQueued')
    await loadRecords()
  } catch { if (version === queueRequest) queueError.value = t('admin.accounts.iqFailed') }
  finally { if (version === queueRequest) queuing.value = false }
}
async function loadRecords() {
  stopRefresh()
  const version = ++request
  const id = props.account?.id
  if (!props.show || !id) return
  loading.value = true
  error.value = ''
  statusError.value = ''
  controller = new AbortController()
  const signal = controller.signal
  await Promise.all([
    adminAPI.accounts.getIQCheckResults(id, signal).then(result => { if (version === request) records.value = result }).catch(() => { if (version === request) error.value = t('admin.accounts.iqHistoryFailed') }),
    adminAPI.accounts.getIQCheckStatus(id, signal).then(result => {
      if (version === request) { state.value = result; emit('updated', result) }
    }).catch(() => { if (version === request) statusError.value = t('admin.accounts.iqStatusFailed') })
  ])
  if (version === request) { loading.value = false; scheduleRefresh() }
}
watch([() => props.show, () => props.account?.id], () => {
  stopRefresh()
  request++; downloadRequest++; queueRequest++
  records.value = []; error.value = ''; downloadError.value = ''; copyMessage.value = ''
  state.value = undefined; statusError.value = ''; queueError.value = ''; queueMessage.value = ''
  loading.value = false; downloading.value = false; queuing.value = false
  void loadRecords()
}, { immediate: true })
onBeforeUnmount(() => { request++; downloadRequest++; queueRequest++; stopRefresh() })
</script>
