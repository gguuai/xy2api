import { beforeEach, describe, expect, it, vi } from 'vitest'
const api = vi.hoisted(() => ({ getSchedulingMode: vi.fn(), setSchedulingMode: vi.fn() }))
vi.mock('@/api/admin/schedulingMode', () => api)
import { useSchedulingModeStore } from '../schedulingMode'
beforeEach(() => { vi.resetAllMocks() })
describe('authoritative scheduling mode', () => {
  it('does not invent a mode when reading fails', async () => {
    api.getSchedulingMode.mockRejectedValue(new Error('unavailable'))
    const store = useSchedulingModeStore(); await store.fetch()
    expect(store.document).toBeNull(); expect(store.failed).toBe(true)
    expect(store.isControlled).toBe(false); expect(store.isSub2API).toBe(false)
  })
  it('deduplicates simultaneous reads and stores the version', async () => {
    api.getSchedulingMode.mockResolvedValue({ mode: 'sub2api', version: 8 })
    const store = useSchedulingModeStore(); await Promise.all([store.fetch(), store.fetch()])
    expect(api.getSchedulingMode).toHaveBeenCalledTimes(1)
    expect(store.isSub2API).toBe(true); expect(store.document?.version).toBe(8)
  })
  it('does not let a late read overwrite a successfully applied mode', async () => {
    let finish!: (value: unknown) => void
    api.getSchedulingMode.mockImplementation(() => new Promise(resolve => { finish = resolve }))
    api.setSchedulingMode.mockResolvedValue({ mode: 'controlled', version: 9 })
    const store = useSchedulingModeStore(); const read = store.fetch()
    await store.save('controlled', 8); finish({ mode: 'sub2api', version: 8 }); await read
    expect(store.document).toEqual({ mode: 'controlled', version: 9 })
    expect(api.setSchedulingMode).toHaveBeenCalledWith('controlled', 8)
  })
  it('keeps authoritative state and propagates conflicts without retrying a write', async () => {
    const store = useSchedulingModeStore(); store.document = { mode: 'sub2api', version: 3 }
    api.setSchedulingMode.mockRejectedValue({ status: 409 })
    await expect(store.save('controlled', 3)).rejects.toEqual({ status: 409 })
    expect(store.document).toEqual({ mode: 'sub2api', version: 3 })
    expect(store.saving).toBe(false); expect(api.setSchedulingMode).toHaveBeenCalledTimes(1)
  })
  it('hides mode-specific controls after a failed refresh', async () => {
    const store = useSchedulingModeStore(); store.document = { mode: 'controlled', version: 2 }
    api.getSchedulingMode.mockRejectedValue(new Error('offline')); await store.fetch(true)
    expect(store.document?.mode).toBe('controlled'); expect(store.isControlled).toBe(false)
  })
  it('discards an old administrator read after logout', async () => {
    let finish!: (value: unknown) => void
    api.getSchedulingMode.mockImplementation(() => new Promise(resolve => { finish = resolve }))
    const store = useSchedulingModeStore(); const read = store.fetch(); store.reset()
    finish({ mode: 'controlled', version: 1 }); await read
    expect(store.document).toBeNull()
  })
})
