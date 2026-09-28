<template>
  <section class="rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-800 sm:p-5">
    <h2 class="font-semibold">{{ t('admin.scheduling.traceTitle') }}</h2>
    <p class="mt-2 text-sm text-gray-500">{{ t('admin.scheduling.traceHint') }}</p>
    <form class="mt-4 flex flex-wrap items-end gap-3" @submit.prevent="query">
      <label class="min-w-0 flex-1 text-sm font-medium">{{ t('admin.scheduling.requestID') }}<input v-model.trim="requestID" class="input mt-1" :disabled="loading" maxlength="200" autocomplete="off" data-testid="trace-request-id"></label>
      <button type="submit" class="btn btn-secondary" :disabled="loading || !requestID" data-testid="trace-query">{{ loading ? t('common.loading') : t('admin.scheduling.queryTrace') }}</button>
    </form>
    <p v-if="error" class="mt-3 text-sm text-red-600" role="alert">{{ error }}</p>
    <div v-if="result" class="mt-4 space-y-3" aria-live="polite">
      <p class="break-all text-sm text-gray-500" data-testid="trace-result-id">{{ t('admin.scheduling.requestID') }}: {{ result.request_id }}</p>
      <p v-if="!result.attempts.length" class="text-sm text-gray-500" data-testid="trace-empty">{{ t('admin.scheduling.traceEmpty') }}</p>
      <article v-for="(attempt, index) in result.attempts" :key="attempt.ticket_id" class="rounded-lg border border-gray-200 p-3 dark:border-dark-600" data-testid="trace-attempt">
        <div class="flex flex-wrap items-center justify-between gap-2"><h3 class="text-sm font-semibold">{{ t('admin.scheduling.traceTicket', { value: index + 1 }) }} · {{ t('admin.scheduling.account') }} #{{ attempt.account_id }}</h3><span class="text-sm">{{ attempt.state }} · {{ attempt.outcome || t('admin.scheduling.unobserved') }}</span></div>
        <p class="mt-1 break-all font-mono text-xs text-gray-500">{{ attempt.ticket_id }}</p>
        <dl class="mt-3 grid gap-3 text-sm sm:grid-cols-2 lg:grid-cols-4">
          <div><dt class="text-gray-500">{{ t('admin.scheduling.priority') }}</dt><dd>{{ attempt.metrics.priority ?? t('admin.scheduling.unobserved') }}</dd></div>
          <div><dt class="text-gray-500">{{ t('admin.scheduling.decision') }}</dt><dd class="break-words">{{ attempt.metrics.reason || t('admin.scheduling.unobserved') }}</dd></div>
          <div><dt class="text-gray-500">{{ t('admin.scheduling.metricVersion') }}</dt><dd>{{ attempt.metrics.metric_version || t('admin.scheduling.unobserved') }}</dd></div>
          <div><dt class="text-gray-500">{{ t('admin.scheduling.stopReason') }}</dt><dd class="break-words">{{ attempt.metrics.stop_reason || t('admin.scheduling.unobserved') }}</dd></div>
          <div v-for="key in timingKeys" :key="key"><dt class="text-gray-500">{{ t('admin.scheduling.traceTiming.' + key) }}</dt><dd :data-testid="'trace-' + index + '-' + key">{{ duration(attempt.metrics[key]) }}</dd></div>
          <div><dt class="text-gray-500">{{ t('admin.scheduling.dispatchedAt') }}</dt><dd>{{ formatDateTime(attempt.dispatched_at) }}</dd></div>
          <div><dt class="text-gray-500">{{ t('admin.scheduling.settledAt') }}</dt><dd>{{ attempt.settled_at ? formatDateTime(attempt.settled_at) : t('admin.scheduling.unobserved') }}</dd></div>
          <div><dt class="text-gray-500">{{ t('admin.scheduling.settlementState') }}</dt><dd>{{ attempt.usage_pending ? t('admin.scheduling.settlementPending') : t('admin.scheduling.settlementDone') }}</dd></div>
        </dl>
        <p v-if="attempt.cancel_requested" class="mt-3 text-sm text-amber-700">{{ t('admin.scheduling.cancelRequested') }}</p>
        <UnknownAttemptResolution v-if="attempt.state === 'unknown'" :ticket-id="attempt.ticket_id" @resolved="query" />
      </article>
    </div>
  </section>
</template>

<script setup lang="ts">
import { onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import schedulingAPI from '@/api/admin/scheduling'
import type { SchedulingRequestAttempts } from '@/types/scheduling'
import { extractApiErrorMessage } from '@/utils/apiError'
import { formatDateTime } from '@/utils/format'
import UnknownAttemptResolution from './UnknownAttemptResolution.vue'

const props = defineProps<{ initialRequestId?: string }>()
const { t } = useI18n()
const requestID = ref(props.initialRequestId || '')
const result = ref<SchedulingRequestAttempts | null>(null)
const loading = ref(false)
const error = ref('')
const timingKeys = ['first_event_ms', 'first_semantic_ms', 'first_answer_ms', 'overall_first_semantic_ms', 'remaining_budget_ms'] as const
let controller: AbortController | undefined
let alive = true
function duration(value: number | undefined): string { return typeof value === 'number' && Number.isFinite(value) ? value + ' ms' : t('admin.scheduling.unobserved') }
async function query() {
  if (!requestID.value.trim() || loading.value) return
  controller?.abort(); controller = new AbortController()
  loading.value = true; error.value = ''; result.value = null
  try {
    const response = await schedulingAPI.getRequestAttempts(requestID.value.trim(), controller.signal)
    if (alive) result.value = response
  } catch (err) { if (alive) error.value = extractApiErrorMessage(err, t('admin.scheduling.traceFailed')) }
  finally { if (alive) loading.value = false }
}
onUnmounted(() => { alive = false; controller?.abort() })
</script>
