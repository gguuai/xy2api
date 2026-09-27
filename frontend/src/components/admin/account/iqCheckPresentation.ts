import type { Account, IQCheckRecord } from '@/types'

type Translate = (key: string, params?: Record<string, string | number>) => string
export type IQDetailRow = { label: string; value: string; raw?: string }
export const iqEffortLabel = (value?: string) => value === 'upstream_default' ? 'default' : value || '—'
export const iqSeconds = (value: number) => (value / 1000).toFixed(2) + ' s'
export const iqProtocolLabel = (value?: string) => ({ responses: 'Responses API', chat_completions: 'Chat Completions API' })[value ?? ''] ?? value ?? ''
const prefix = 'admin.accounts.'

export function iqNextLabel(state: NonNullable<Account['iq_check']>, t: Translate, date: (value: string) => string) {
  if (!state.enabled) return t(prefix + 'iqOff')
  if (state.execution_state === 'running') return t(prefix + 'iqRunning')
  if (state.execution_state === 'paused') return t(prefix + 'iqNeedsAttention')
  return state.next_eligible_at ? date(state.next_eligible_at) : t(prefix + 'iqWaiting')
}

export function iqDiagnosticSummary(record: IQCheckRecord, t: Translate): IQDetailRow[] {
  const d = record.diagnostic
  const rows: IQDetailRow[] = []
  const request = [iqProtocolLabel(record.protocol || d?.protocol), d?.http_status ? `HTTP ${d.http_status}` : ''].filter(Boolean)
  if (request.length) rows.push({ label: t(prefix + 'iqRequest'), value: request.join(' · ') })
  if (record.output_mode) rows.push({ label: t(prefix + 'iqOutputMode'), value: record.output_mode === 'strict' ? t(prefix + 'iqStrict') : record.output_mode === 'compat' ? t(prefix + 'iqCompat') : record.output_mode })
  if (record.reported_model && record.reported_model !== record.model) rows.push({ label: t(prefix + 'iqReportedModel'), value: record.reported_model })
  const timings: string[] = []
  if (d?.first_byte_ms != null) timings.push(t(prefix + 'iqFirstByteValue', { value: iqSeconds(d.first_byte_ms) }))
  if (d?.total_ms != null) timings.push(t(prefix + 'iqRequestTimeValue', { value: iqSeconds(d.total_ms) }))
  if (timings.length) rows.push({ label: t(prefix + 'iqRequestTiming'), value: timings.join(' · ') })
  const tokens: string[] = []
  if (d?.input_tokens != null) tokens.push(t(prefix + 'iqInputTokensValue', { value: d.input_tokens }))
  if (d?.output_tokens != null) {
    let output = t(prefix + 'iqOutputTokensValue', { value: d.output_tokens })
    if (d.reasoning_tokens != null) output += t(prefix + 'iqReasoningIncluded', { value: d.reasoning_tokens })
    tokens.push(output)
  } else if (d?.reasoning_tokens != null) tokens.push(t(prefix + 'iqReasoningTokensValue', { value: d.reasoning_tokens }))
  if (tokens.length) rows.push({ label: t(prefix + 'iqTokenUsage'), value: tokens.join(' · ') })
  return rows
}

export function iqTechnicalGroups(record: IQCheckRecord, t: Translate, exists: (key: string) => boolean) {
  const d = record.diagnostic
  const groups: { label: string; rows: IQDetailRow[] }[] = []
  const translated = (group: string, value: string) => {
    const key = prefix + group + '.' + value
    return exists(key) ? t(key) : value
  }
  const row = (key: string, value: unknown): IQDetailRow[] => {
    if (value == null || value === '') return []
    const raw = String(value)
    let display = raw
    if (key === 'category') display = translated('iqCategories', raw)
    if (key === 'limit_kind') display = translated('iqLimits', raw)
    if (key === 'stage') display = translated('iqStages', raw)
    if (key === 'answer_source') display = translated('iqAnswerSources', raw)
    if (key === 'retry_visibility') display = translated('iqRetryVisibility', raw)
    if (key === 'code') display = translated('iqReasons', raw)
    if (key === 'transport') display = raw === 'http' ? 'HTTP' : raw === 'plugin' ? t(prefix + 'iqPlugin') : raw
    if (key === 'media_type') display = ({ 'text/event-stream': 'SSE', 'application/json': 'JSON' })[raw] ?? raw
    if (key === 'format_detected' || key === 'retry_after_unbounded') display = t(prefix + (value ? 'iqYes' : 'iqNo'))
    if (key === 'bytes_read' || key === 'limit_bytes') display = `${Number(value).toLocaleString()} B`
    return [{ label: t(prefix + 'iqDiagnosticLabels.' + key), value: display, raw }]
  }
  for (const [label, keys] of [
    ['iqRequest', ['request_id', 'media_type', 'content_encoding', 'transport', 'retry_visibility', 'error_code', 'error_type', 'retry_after', 'retry_after_unbounded']],
    ['iqParsing', ['category', 'limit_kind', 'limit_bytes', 'stage', 'answer_source', 'done_messages', 'terminal_items', 'ignored_items', 'ignored_types', 'event_type', 'event_index', 'bytes_read', 'field', 'offset', 'format_detected']]
  ] as const) {
    const rows = keys.flatMap(key => row(key, d?.[key]))
    if (label === 'iqParsing' && d?.code && d.code !== 'correct_answer') rows.unshift(...row('code', d.code))
    if (rows.length) groups.push({ label: t(prefix + label), rows })
  }
  const versions: IQDetailRow[] = []
  if (record.grader_version) versions.push({ label: t(prefix + 'iqGrader'), value: record.grader_version, raw: record.grader_version })
  versions.push(...row('parser_version', d?.parser_version))
  if (versions.length) groups.push({ label: t(prefix + 'iqVersions'), rows: versions })
  return groups
}
