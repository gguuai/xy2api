<template>
  <section ref="rootElement" class="card" data-testid="scheduling-mode-settings">
    <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('admin.scheduling.modeSettings.title') }}</h2>
      <p class="mt-1 text-sm text-gray-600 dark:text-dark-300">{{ t('admin.scheduling.modeSettings.hint') }}</p>
    </div>
    <div class="space-y-4 p-6">
      <p v-if="!store.document && store.loading" class="text-sm text-gray-600 dark:text-dark-300" role="status">{{ t('common.loading') }}</p>
      <div v-if="store.failed" class="flex flex-wrap items-center justify-between gap-3 text-sm" role="alert">
        <span class="text-red-700 dark:text-red-300">{{ t('admin.scheduling.modeSettings.loadFailed') }}</span>
        <button type="button" class="btn btn-secondary" :disabled="store.loading" @click="reload">{{ t('common.refresh') }}</button>
      </div>
      <fieldset v-if="store.document" :disabled="store.saving || store.loading || store.failed" class="min-w-0 space-y-3">
        <legend class="sr-only">{{ t('admin.scheduling.modeSettings.title') }}</legend>
        <label v-for="mode in modes" :key="mode" class="flex cursor-pointer items-start gap-3 rounded-xl border p-4 focus-within:ring-2 focus-within:ring-primary-500" :class="selected === mode ? 'border-primary-500 bg-primary-50/50 dark:bg-primary-950/20' : 'border-gray-200 dark:border-dark-600'">
          <input v-model="selected" type="radio" name="scheduling-mode" :value="mode" class="mt-1 h-4 w-4 accent-primary-600" :data-testid="'mode-' + mode" @change="notice = ''; error = ''">
          <span class="min-w-0"><span class="flex flex-wrap items-center gap-2 text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.scheduling.modeSettings.' + mode) }}<span v-if="store.document.mode === mode" class="text-xs font-normal text-primary-700 dark:text-primary-300">{{ t('admin.scheduling.modeSettings.active') }}</span></span><span class="mt-1 block text-sm text-gray-600 dark:text-dark-300">{{ t('admin.scheduling.modeSettings.' + mode + 'Hint') }}</span></span>
        </label>
      </fieldset>
      <p v-if="error" role="alert" class="text-sm text-red-700 dark:text-red-300">{{ error }}</p>
      <p v-if="notice" ref="noticeElement" role="status" class="text-sm text-emerald-700 dark:text-emerald-300">{{ notice }}</p>
      <div v-if="store.document" class="flex flex-wrap items-center justify-between gap-3">
        <RouterLink v-if="store.isControlled" to="/admin/scheduling" class="text-sm font-medium text-primary-700 underline-offset-4 hover:underline dark:text-primary-300">{{ t('admin.scheduling.modeSettings.configure') }}</RouterLink>
        <span v-else class="text-sm text-gray-600 dark:text-dark-300">{{ t('admin.scheduling.modeSettings.originalSettings') }}</span>
        <button type="button" class="btn btn-primary" :disabled="!dirty || store.saving || store.loading || store.failed" data-testid="save-scheduling-mode" @click="save">{{ store.saving ? t('common.saving') : t('admin.scheduling.modeSettings.apply') }}</button>
      </div>
    </div>
  </section>
</template>
<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useSchedulingFeedback } from '@/composables/useSchedulingFeedback'
import { useI18n } from 'vue-i18n'
import { useSchedulingModeStore } from '@/stores/schedulingMode'
import type { SchedulingMode } from '@/api/admin/schedulingMode'
import { isGroupSchedulingConflict } from '@/utils/groupScheduling'
import { extractApiErrorMessage } from '@/utils/apiError'
const { t } = useI18n()
const store = useSchedulingModeStore()
const modes: SchedulingMode[] = ['sub2api', 'controlled']
const selected = ref<SchedulingMode>('controlled')
const error = ref('')
const notice = ref('')
const rootElement = ref<HTMLElement | null>(null)
const noticeElement = ref<HTMLElement | null>(null)
const feedback = useSchedulingFeedback(rootElement)
const dirty = computed(() => store.document !== null && selected.value !== store.document.mode)
watch(() => store.document, (value, previous) => { if (value && !previous) selected.value = value.mode }, { immediate: true })
async function reload() { await store.fetch(true) }
async function save() {
  if (!store.document || !dirty.value || store.saving) return
  error.value = ''; notice.value = ''
  try { await store.save(selected.value, store.document.version); notice.value = t('admin.scheduling.modeSettings.saved'); await nextTick(); feedback(noticeElement.value) }
  catch (cause) {
    if (isGroupSchedulingConflict(cause)) {
      await store.fetch(true)
      error.value = t('admin.scheduling.modeSettings.conflict')
    } else error.value = extractApiErrorMessage(cause, t('admin.scheduling.saveFailed'))
  }
}
onMounted(() => { void store.fetch() })
</script>
