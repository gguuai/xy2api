<template>
  <form class="mt-2 space-y-2 rounded border border-gray-200 p-2 dark:border-dark-600" @submit.prevent="submit">
    <p>{{ t('admin.failureDomains.recoveryHint') }}</p>
    <label class="block">{{ t('admin.failureDomains.source') }}
      <select v-model="source" class="input" :disabled="busy">
        <option value="provider_status">{{ t('admin.failureDomains.providerStatus') }}</option>
        <option value="provider_receipt">{{ t('admin.failureDomains.providerReceipt') }}</option>
        <option value="operator_verified_log">{{ t('admin.failureDomains.verifiedLog') }}</option>
      </select>
    </label>
    <label class="block">{{ t('admin.failureDomains.reference') }}<input v-model.trim="reference" class="input" required minlength="4" maxlength="512" :disabled="busy"></label>
    <label class="block">{{ t('admin.failureDomains.observedAt') }}<input v-model="observedAt" class="input" type="datetime-local" step="1" required :disabled="busy"></label>
    <label class="flex gap-2"><input v-model="verified" type="checkbox" required :disabled="busy">{{ t('admin.failureDomains.recoveryConfirm') }}</label>
    <p v-if="error" role="alert" class="text-red-600">{{ error }}</p>
    <button type="submit" class="btn btn-secondary" :disabled="busy || !verified || !reference || !observedAt">{{ t('admin.failureDomains.recoverySubmit') }}</button>
  </form>
</template>
<script setup lang="ts">
import { onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { permitFailureRecovery } from '@/api/admin/schedulingDomains'
import type { FailureGate, ResolutionEvidence } from '@/api/admin/schedulingDomains'
import { extractApiErrorMessage } from '@/utils/apiError'
const props = defineProps<{ accountId: number; gate: FailureGate; model: string; domainVersion: number }>()
const emit = defineEmits<{ recovered: [] }>()
const { t } = useI18n()
const source = ref<ResolutionEvidence['source']>('provider_status')
const reference = ref(''); const observedAt = ref(''); const verified = ref(false); const busy = ref(false); const error = ref('')
let generation = 0
watch(() => [props.accountId, props.gate.key, props.gate.version, props.model, props.domainVersion], () => { generation++; busy.value = false; verified.value = false; reference.value = ''; observedAt.value = ''; error.value = '' })
onUnmounted(() => { generation++ })
async function submit() {
  if (busy.value || !verified.value || reference.value.length < 4 || !observedAt.value) return
  const run = generation; const timestamp = new Date(observedAt.value)
  if (!Number.isFinite(timestamp.getTime())) return
  busy.value = true; error.value = ''
  try {
    await permitFailureRecovery(props.accountId, { gate_key: props.gate.key, model: props.model, expected_version: props.gate.version, domain_version: props.domainVersion, source: source.value, reference: reference.value, observed_at: timestamp.toISOString() })
    if (run === generation) emit('recovered')
  } catch (e) { if (run === generation) error.value = extractApiErrorMessage(e, t('admin.failureDomains.conflict')) }
  finally { if (run === generation) busy.value = false }
}
</script>
