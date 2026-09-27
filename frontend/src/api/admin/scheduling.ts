import { apiClient } from '../client'
import type { AccountSchedulingControl, SchedulingControlCommand, SchedulingControlScope, SchedulingExplainRequest, SchedulingExplanation, SchedulingPolicy, SchedulingPolicyDocument, SchedulingRequestAttempts, SchedulingTrafficStats } from '@/types/scheduling'

export async function getPolicy(groupID: number, model: string, signal?: AbortSignal): Promise<SchedulingPolicyDocument> {
  const { data } = await apiClient.get<SchedulingPolicyDocument>('/admin/scheduling/policies', { params: { group_id: groupID, model }, signal })
  return data
}
export async function savePolicy(policy: SchedulingPolicy, expectedVersion: number): Promise<SchedulingPolicyDocument> {
  const { data } = await apiClient.put<SchedulingPolicyDocument>('/admin/scheduling/policies', {
    group_id: policy.group_id, model: policy.model, expected_version: expectedVersion, policy
  })
  return data
}
export async function explain(request: SchedulingExplainRequest, signal?: AbortSignal): Promise<SchedulingExplanation> {
  const { data } = await apiClient.post<SchedulingExplanation>('/admin/scheduling/explain', request, { signal })
  return data
}
export async function getControl(accountID: number, scope: SchedulingControlScope = 'logical_account', signal?: AbortSignal): Promise<AccountSchedulingControl> {
  const { data } = await apiClient.get<AccountSchedulingControl>('/admin/accounts/' + accountID + '/scheduling-control', { params: { scope }, signal })
  return data
}
export async function setControl(accountID: number, command: SchedulingControlCommand): Promise<AccountSchedulingControl> {
  const { data } = await apiClient.post<AccountSchedulingControl>('/admin/accounts/' + accountID + '/scheduling-control', command)
  return data
}
export async function getRequestAttempts(requestID: string, signal?: AbortSignal): Promise<SchedulingRequestAttempts> {
  const { data } = await apiClient.get<SchedulingRequestAttempts>('/admin/scheduling/requests/' + encodeURIComponent(requestID) + '/attempts', { signal })
  return data
}
export async function getStats(groupID: number, model: string, since: string, signal?: AbortSignal): Promise<SchedulingTrafficStats> {
  const { data } = await apiClient.get<SchedulingTrafficStats>('/admin/scheduling/stats', { params: { group_id: groupID, model, since }, signal })
  return data
}
export default { getPolicy, savePolicy, explain, getControl, setControl, getRequestAttempts, getStats }
