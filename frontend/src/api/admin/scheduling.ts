import { apiClient } from '../client'
import type { GroupSchedulingPolicy, GroupSchedulingDocument } from '@/types/scheduling'

export async function getGroupPolicy(groupID: number, signal?: AbortSignal): Promise<GroupSchedulingDocument> {
  const { data } = await apiClient.get<GroupSchedulingDocument>(`/admin/scheduling/groups/${groupID}`, { signal })
  return data
}
export async function saveGroupPolicy(policy: GroupSchedulingPolicy, expectedVersion: number): Promise<GroupSchedulingDocument> {
  const { data } = await apiClient.put<GroupSchedulingDocument>(`/admin/scheduling/groups/${policy.group_id}`, { expected_version: expectedVersion, policy })
  return data
}

export default { getGroupPolicy, saveGroupPolicy }
