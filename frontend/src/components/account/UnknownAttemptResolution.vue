<template>
  <div class="mt-3 rounded-lg border border-amber-200 p-3">
    <button type="button" class="btn btn-secondary" :disabled="busy" @click="load">{{ t('admin.failureDomains.inspect') }}</button>
    <p v-if="error" role="alert" class="mt-2 text-sm text-red-600">{{ error }}</p>
    <div v-if="detail" class="mt-3 space-y-3">
      <p class="text-sm">{{ detail.state }} · {{ t('admin.failureDomains.version', { value: detail.version }) }}</p>
      <p class="text-xs text-gray-500">{{ t('admin.failureDomains.evidenceHint') }}</p>
      <template v-if="detail.state === 'unknown'">
        <label class="block text-sm">{{ t('admin.failureDomains.source') }}<select v-model="source" class="input mt-1" :disabled="busy"><option value="provider_status">{{ t('admin.failureDomains.providerStatus') }}</option><option value="provider_receipt">{{ t('admin.failureDomains.providerReceipt') }}</option><option value="operator_verified_log">{{ t('admin.failureDomains.verifiedLog') }}</option></select></label>
        <label class="block text-sm">{{ t('admin.failureDomains.reference') }}<input v-model.trim="reference" class="input mt-1" maxlength="512" :disabled="busy"></label>
        <label class="block text-sm">{{ t('admin.failureDomains.observedAt') }}<input v-model="observedAt" type="datetime-local" step="1" class="input mt-1" :disabled="busy"></label>
        <label class="block text-sm">{{ t('admin.failureDomains.outcome') }}<select v-model="outcome" class="input mt-1" :disabled="busy"><option value="completed">{{ t('admin.failureDomains.completed') }}</option><option value="upstream_error">{{ t('admin.failureDomains.upstreamError') }}</option><option value="cancelled">{{ t('admin.failureDomains.remoteCancelled') }}</option><option value="not_sent">{{ t('admin.failureDomains.notSent') }}</option></select></label>
        <label class="flex items-start gap-2 text-sm"><input v-model="confirmed" type="checkbox" :disabled="busy">{{ t('admin.failureDomains.confirm') }}</label>
        <button type="button" class="btn btn-primary" :disabled="!canSubmit" @click="resolve">{{ t('admin.failureDomains.resolve') }}</button>
      </template>
      <p class="text-xs text-gray-500">{{ t('admin.failureDomains.auditCount', { value: detail.audit.length }) }}</p>
    </div>
  </div>
</template>
<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getUnknownAttempt, resolveUnknownAttempt } from '@/api/admin/schedulingDomains'
import type { ResolutionEvidence, UnknownDetail } from '@/api/admin/schedulingDomains'
import { extractApiErrorMessage } from '@/utils/apiError'
const props = defineProps<{ ticketId: string }>()
const emit = defineEmits<{ resolved: [] }>()
const { t } = useI18n()
const detail = ref<UnknownDetail | null>(null)
const busy = ref(false); const error = ref(''); const reference = ref(''); const observedAt = ref(''); const confirmed = ref(false)
const source = ref<ResolutionEvidence['source']>('provider_status'); const outcome = ref<ResolutionEvidence['outcome']>('completed')
let alive = true
let generation = 0
watch(() => props.ticketId, () => { generation++; busy.value=false; detail.value=null; confirmed.value=false; reference.value=''; observedAt.value=''; error.value='' })
const canSubmit = computed(() => detail.value?.state === 'unknown' && !busy.value && confirmed.value && reference.value.length >= 4 && Number.isFinite(Date.parse(observedAt.value)))
async function load() { if (busy.value) return; const run=generation; busy.value = true; error.value = ''; try { const value = await getUnknownAttempt(props.ticketId); if (alive && run===generation) detail.value = value } catch (e) { if (alive && run===generation) error.value = extractApiErrorMessage(e, t('admin.failureDomains.failed')) } finally { if (alive && run===generation) busy.value = false } }
async function resolve() {
  if (!canSubmit.value || !detail.value) return
  const run=generation
  busy.value = true; error.value = ''
  try {
    const value = await resolveUnknownAttempt(props.ticketId, { expected_version: detail.value.version, source: source.value, reference: reference.value, observed_at: new Date(observedAt.value).toISOString(), outcome: outcome.value, usage_pending: outcome.value !== 'not_sent' })
    if (alive && run===generation) { detail.value = value; confirmed.value = false; emit('resolved') }
  } catch (e) { if (alive && run===generation) { detail.value = null; confirmed.value = false; error.value = extractApiErrorMessage(e, t('admin.failureDomains.conflict')) } }
  finally { if (alive && run===generation) busy.value = false }
}
onUnmounted(() => { alive = false })
</script>
