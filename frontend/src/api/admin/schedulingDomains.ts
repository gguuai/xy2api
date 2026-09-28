import apiClient from '../client'
export interface FailureDomains { account_id: number; version: number; quota_pool_id: string; availability_pool_id: string }
export interface FailureGate { key: string; scope: string; reason: string; version: number; blocked: boolean; ready_after?: string; probe_active: boolean }
export interface FailureState { domains: FailureDomains; gates: FailureGate[]; eligible: boolean }
export interface UnknownDetail { ticket_id: string; account_id: number; state: string; outcome: string; version: number; dispatched_at: string; metrics: Record<string, unknown>; audit: Record<string, unknown>[] }
export interface ResolutionEvidence { expected_version: number; source: 'provider_status' | 'provider_receipt' | 'operator_verified_log'; reference: string; observed_at: string; outcome: 'completed' | 'upstream_error' | 'cancelled' | 'not_sent'; usage_pending: boolean }
export async function getFailureDomains(id: number, model = '', signal?: AbortSignal): Promise<FailureState> {
  const { data } = await apiClient.get<FailureState>('/admin/accounts/' + id + '/failure-domains', { params: { model }, signal }); return data
}
export async function putFailureDomains(value: FailureDomains): Promise<FailureDomains> {
  const { data } = await apiClient.put<FailureDomains>('/admin/accounts/' + value.account_id + '/failure-domains', { expected_version: value.version, quota_pool_id: value.quota_pool_id, availability_pool_id: value.availability_pool_id }); return data
}
export async function getUnknownAttempt(ticket: string): Promise<UnknownDetail> {
  const { data } = await apiClient.get<UnknownDetail>('/admin/scheduling/attempts/' + encodeURIComponent(ticket) + '/resolution'); return data
}
export async function resolveUnknownAttempt(ticket: string, evidence: ResolutionEvidence): Promise<UnknownDetail> {
  const { data } = await apiClient.post<UnknownDetail>('/admin/scheduling/attempts/' + encodeURIComponent(ticket) + '/resolution', evidence); return data
}

export interface FailureRecoveryEvidence { gate_key: string; model: string; expected_version: number; domain_version: number; source: ResolutionEvidence['source']; reference: string; observed_at: string }
export async function permitFailureRecovery(id: number, evidence: FailureRecoveryEvidence): Promise<FailureState> {
  const { data } = await apiClient.post<FailureState>('/admin/accounts/' + id + '/failure-domains/recovery', evidence); return data
}
