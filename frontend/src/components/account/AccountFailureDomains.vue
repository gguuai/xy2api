<template>
  <details class="rounded-lg border border-gray-200 p-3 dark:border-dark-600" @toggle="onToggle">
    <summary class="cursor-pointer text-sm font-medium">{{ t('admin.failureDomains.title') }}</summary>
    <p class="mt-2 text-sm text-gray-500">{{ t('admin.failureDomains.hint') }}</p>
    <p v-if="error" class="mt-2 text-sm text-red-600" role="alert">{{ error }}</p>
    <button v-if="error" type="button" class="btn btn-secondary mt-2" :disabled="busy" @click="load">{{ t('common.refresh') }}</button>
    <div v-if="domains" class="mt-3 space-y-3">
      <label class="block text-sm">{{ t('admin.failureDomains.quota') }}<input v-model.trim="domains.quota_pool_id" class="input mt-1" maxlength="128" :disabled="busy" data-testid="quota-pool-id"></label>
      <label class="block text-sm">{{ t('admin.failureDomains.availability') }}<input v-model.trim="domains.availability_pool_id" class="input mt-1" maxlength="128" :disabled="busy" data-testid="availability-pool-id"></label>
      <p class="text-xs text-gray-500">{{ t('admin.failureDomains.version', { value: domains.version }) }}</p>
      <button type="button" class="btn btn-secondary" :disabled="busy" @click="save">{{ t('common.save') }}</button>
      <label class="block text-sm">{{ t('admin.failureDomains.model') }}<input v-model.trim="model" class="input mt-1" maxlength="200" :disabled="busy"></label>
      <button type="button" class="btn btn-secondary" :disabled="busy" @click="load">{{ t('common.refresh') }}</button>
      <ul class="space-y-1 text-xs">
        <li v-for="gate in gates" :key="gate.key" class="break-words">{{ gate.scope }} · {{ gate.reason }} · {{ gate.blocked ? t('admin.failureDomains.hard') : gate.ready_after || t('admin.failureDomains.ready') }}<span v-if="gate.probe_active"> · {{ t('admin.failureDomains.probe') }}</span><details v-if="(gate.blocked || gate.ready_after) && !gate.probe_active"><summary class="cursor-pointer">{{ t('admin.failureDomains.recoverySubmit') }}</summary><FailureGateRecovery :account-id="accountId" :gate="gate" :model="model" :domain-version="domains.version" @recovered="load" /></details></li>
      </ul>
    </div>
  </details>
</template>
<script setup lang="ts">
import FailureGateRecovery from './FailureGateRecovery.vue'
import { onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getFailureDomains, putFailureDomains } from '@/api/admin/schedulingDomains'
import type { FailureDomains, FailureGate } from '@/api/admin/schedulingDomains'
import { extractApiErrorMessage } from '@/utils/apiError'
const props = defineProps<{ accountId: number }>()
const { t } = useI18n()
const domains = ref<FailureDomains | null>(null)
const gates = ref<FailureGate[]>([])
const model = ref('')
const error = ref('')
const busy = ref(false)
let generation = 0
let open = false
async function load() {
  if (!open || busy.value) return
  const id = props.accountId; const run = ++generation
  busy.value = true; error.value = ''
  try { const result = await getFailureDomains(id, model.value); if (run !== generation) return; domains.value = result.domains; gates.value = result.gates.filter(g => g.reason !== 'open') }
  catch (e) { if (run === generation) { domains.value = null; error.value = extractApiErrorMessage(e, t('admin.failureDomains.failed')) } }
  finally { if (run === generation) busy.value = false }
}
async function save() {
  if (!domains.value || busy.value) return
  busy.value = true; error.value = ''; const run = generation
  try { const result = await putFailureDomains({ ...domains.value }); if (run === generation) { domains.value = result; gates.value = [] } }
  catch (e) { if (run === generation) { domains.value = null; error.value = extractApiErrorMessage(e, t('admin.failureDomains.conflict')) } }
  finally { if (run === generation) busy.value = false }
}
function onToggle(event: Event) { const next=(event.target as HTMLDetailsElement).open; if (next===open) return; open=next; if (open && !domains.value) void load() }
watch(() => props.accountId, () => { generation++; busy.value = false; domains.value = null; gates.value = []; error.value = ''; if (open) void load() })
onUnmounted(() => { generation++ })
</script>
