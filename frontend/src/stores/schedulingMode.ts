import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { getSchedulingMode, setSchedulingMode } from '@/api/admin/schedulingMode'
import type { SchedulingMode, SchedulingModeDocument } from '@/api/admin/schedulingMode'

/** Read the authoritative mode; a failed read must not invent an active mode. */
export const useSchedulingModeStore = defineStore('schedulingMode', () => {
  const document = ref<SchedulingModeDocument | null>(null)
  const loading = ref(false)
  const saving = ref(false)
  const failed = ref(false)
  let revision = 0
  let pending: Promise<void> | null = null
  const isControlled = computed(() => !failed.value && document.value?.mode === 'controlled')
  const isSub2API = computed(() => !failed.value && document.value?.mode === 'sub2api')

  function accept(value: SchedulingModeDocument) {
    if (!['sub2api', 'controlled'].includes(value.mode) || !Number.isSafeInteger(value.version) || value.version < 0) throw new Error('Invalid scheduling mode response')
    document.value = value
    failed.value = false
  }

  async function fetch(force = false): Promise<void> {
    if (pending) { await pending; if (force) return fetch(true); return }
    if (document.value && !force) return
    const readRevision = revision
    loading.value = true
    pending = (async () => {
      try {
        const value = await getSchedulingMode()
        if (readRevision === revision) accept(value)
      } catch {
        if (readRevision === revision) failed.value = true
      } finally { loading.value = false; pending = null }
    })()
    return pending
  }

  async function save(mode: SchedulingMode, expectedVersion: number) {
    if (saving.value) return
    saving.value = true
    revision++
    try { accept(await setSchedulingMode(mode, expectedVersion)) }
    finally { saving.value = false }
  }

  function reset() { revision++; document.value = null; failed.value = false }
  return { document, loading, saving, failed, isControlled, isSub2API, fetch, save, reset }
})
