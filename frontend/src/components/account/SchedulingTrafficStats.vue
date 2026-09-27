<template>
  <section class="rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-800 sm:p-5">
    <div class="flex flex-wrap items-center justify-between gap-3"><h2 class="font-semibold">{{ t('admin.scheduling.statsTitle') }}</h2><button type="button" class="btn btn-secondary" :disabled="loading" data-testid="query-stats" @click="query">{{ loading ? t('common.loading') : t('admin.scheduling.queryStats') }}</button></div>
    <p class="mt-2 text-sm text-gray-500">{{ t('admin.scheduling.statsHint') }}</p>
    <p v-if="error" role="alert" class="mt-3 text-sm text-red-600">{{ error }}</p>
    <div v-if="stats" class="mt-4" aria-live="polite">
      <p class="text-sm text-gray-500">{{ formatDateTime(stats.since) }} → {{ formatDateTime(stats.until) }} · {{ stats.model }} · {{ t('admin.scheduling.firstTotal') }} {{ stats.ordinary_first_total }}</p>
      <p v-if="!stats.accounts.length" class="mt-3 text-sm text-gray-500" data-testid="stats-empty">{{ t('admin.scheduling.statsEmpty') }}</p>
      <div v-else class="mt-3 overflow-x-auto"><table class="w-full text-left text-sm"><thead><tr class="border-b border-gray-200 dark:border-dark-600"><th class="p-2">{{ t('admin.scheduling.account') }}</th><th class="p-2">{{ t('admin.scheduling.targetShare') }}</th><th class="p-2">{{ t('admin.scheduling.actualShare') }}</th><th class="p-2">{{ t('admin.scheduling.firstTotal') }}</th><th v-for="kind in exceptionKinds" :key="kind" class="p-2">{{ t('admin.scheduling.dispatchKinds.' + kind) }}</th></tr></thead><tbody><tr v-for="account in stats.accounts" :key="account.account_id" class="border-b border-gray-100 dark:border-dark-700" data-testid="stats-account"><td class="p-2">#{{ account.account_id }}</td><td class="p-2" :data-testid="'stats-target-' + account.account_id">{{ percentage(targets.get(account.account_id)) }}</td><td class="p-2" :data-testid="'stats-actual-' + account.account_id">{{ percentage(account.ordinary_first_share) }}</td><td class="p-2">{{ account.ordinary_first }}</td><td v-for="kind in exceptionKinds" :key="kind" class="p-2">{{ account.by_kind[kind] ?? t('admin.scheduling.unobserved') }}</td></tr></tbody></table></div>
      <p class="mt-3 text-xs text-gray-500">{{ t('admin.scheduling.statsTargetHint') }}</p>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import schedulingAPI from '@/api/admin/scheduling'
import type { SchedulingExplainCandidate, SchedulingTrafficStats as TrafficStats } from '@/types/scheduling'
import { extractApiErrorMessage } from '@/utils/apiError'
import { formatDateTime } from '@/utils/format'

const props = defineProps<{ groupId: number; model: string; candidates?: SchedulingExplainCandidate[] }>()
const { t } = useI18n()
const stats = ref<TrafficStats | null>(null)
const loading = ref(false)
const error = ref('')
const exceptionKinds = ['retry', 'probe', 'pin', 'owner', 'fallback'] as const
const targets = computed(() => new Map((props.candidates || []).map(candidate => [candidate.account_id, candidate.target_share])))
let controller: AbortController | undefined
let generation = 0
function percentage(value: number | null | undefined): string { return typeof value === 'number' && Number.isFinite(value) ? (value * 100).toFixed(1) + '%' : t('admin.scheduling.unobserved') }
async function query() {
  if (loading.value || !props.model) return
  controller?.abort(); controller = new AbortController()
  const current = ++generation
  loading.value = true; error.value = ''; stats.value = null
  try {
    const result = await schedulingAPI.getStats(props.groupId, props.model, new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString(), controller.signal)
    if (current === generation) stats.value = result
  } catch (err) { if (current === generation) error.value = extractApiErrorMessage(err, t('admin.scheduling.statsFailed')) }
  finally { if (current === generation) loading.value = false }
}
watch(() => [props.groupId, props.model], () => { generation++; controller?.abort(); stats.value = null; error.value = ''; loading.value = false })
onUnmounted(() => { generation++; controller?.abort() })
</script>
