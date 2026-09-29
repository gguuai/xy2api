
export type SchedulingMode = 'sub2api' | 'controlled'
export interface SchedulingModeDocument { mode: SchedulingMode; version: number }

export async function getSchedulingMode(): Promise<SchedulingModeDocument> {
  const { apiClient } = await import('../client')
  const { data } = await apiClient.get<SchedulingModeDocument>('/admin/scheduling/mode')
  return data
}

export async function setSchedulingMode(mode: SchedulingMode, expectedVersion: number): Promise<SchedulingModeDocument> {
  const { apiClient } = await import('../client')
  const { data } = await apiClient.put<SchedulingModeDocument>('/admin/scheduling/mode', { mode, expected_version: expectedVersion })
  return data
}
