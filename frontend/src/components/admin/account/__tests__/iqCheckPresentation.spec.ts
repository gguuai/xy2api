import { describe, expect, it } from 'vitest'
import { createI18n } from 'vue-i18n'
import { baseCompile } from '@intlify/message-compiler'
const messageCompiler = (message: unknown) => new Function('return ' + baseCompile(String(message), { mode: 'normal' }).code)()
import zh from '@/i18n/locales/zh/admin/accounts'
import en from '@/i18n/locales/en/admin/accounts'
import { iqDiagnosticSummary, iqEffortLabel, iqTechnicalGroups } from '../iqCheckPresentation'
import type { IQCheckRecord } from '@/types'

const record: IQCheckRecord = {
 id: 1, prompt_version: 'candy-v2', model: 'gpt-6-astra', reported_model: 'gpt-6-astra', effort: 'high', status: 'smart', answer: '{"answer":21}', started_at: '2026-09-15T14:04:40Z', finished_at: '2026-09-15T14:04:51Z', latency_ms: 11084, protocol: 'responses', output_mode: 'compat', grader_version: 'candy-grader-v2',
 diagnostic: { parser_version: 'iq-response-v4', stage: 'grade', code: 'correct_answer', protocol: 'responses', http_status: 200, first_byte_ms: 1891, total_ms: 10882, input_tokens: 243, output_tokens: 231, reasoning_tokens: 220, media_type: 'text/event-stream', retry_visibility: 'single_attempt', answer_source: 'terminal_output', request_id: 'example-id', event_index: 15 }
}
describe('IQ presentation', () => {
 it.each(['zh', 'en'])('identifies the precise size limit in %s', locale => {
  const i18n = createI18n({ messageCompiler, legacy: false, locale, messages: { zh: { admin: zh }, en: { admin: en } } })
  const rows = iqTechnicalGroups({ ...record, diagnostic: { parser_version: 'iq-response-v5', stage: 'decode', code: 'response_too_large', category: 'protocol', limit_kind: 'response_bytes', limit_bytes: 8388608, bytes_read: 8388609 } }, i18n.global.t, i18n.global.te).flatMap(group => group.rows)
  expect(rows.find(row => row.raw === 'response_bytes')?.value).not.toBe('response_bytes')
  expect(rows.find(row => row.raw === '8388608')?.value).toBe('8,388,608 B')
  expect(rows.find(row => row.raw === 'protocol')?.value).not.toBe('protocol')
 })
 it('groups real diagnostic fields, keeps timing scopes and deduplicates the model/protocol', () => {
  const i18n = createI18n({ messageCompiler, legacy: false, locale: 'zh', messages: { zh: { admin: zh } } })
  const rows = iqDiagnosticSummary(record, i18n.global.t)
  expect(rows).toHaveLength(4)
  expect(rows.map(row => row.value).join(' ')).toContain('输出 231（含推理 220）')
  expect(rows.map(row => row.value).join('\n')).toContain('Responses API · HTTP 200')
  expect(rows.map(row => row.value).join('\n')).toContain('1.89 s')
  expect(rows.map(row => row.value).join('\n')).toContain('10.88 s')
  expect(rows.map(row => row.value).join('\n')).not.toContain('11.08 s')
  expect(rows.map(row => row.value).join('\n')).not.toContain('gpt-6-astra')
 })
 it.each(['zh', 'en'])('retains raw technical codes while translating %s labels', locale => {
  const i18n = createI18n({ messageCompiler, legacy: false, locale, messages: { zh: { admin: zh }, en: { admin: en } } })
  const groups = iqTechnicalGroups(record, i18n.global.t, i18n.global.te)
  const rows = groups.flatMap(group => group.rows)
  expect(rows.some(row => row.raw === 'terminal_output')).toBe(true)
  expect(rows.some(row => row.raw === 'correct_answer')).toBe(false)
  expect(rows.some(row => row.raw === 'single_attempt')).toBe(true)
  expect(rows.some(row => row.raw === 'example-id')).toBe(true)
 })
 it('does not invent missing historical metadata and preserves unknown enum values', () => {
  const t = (key: string) => key
  expect(iqTechnicalGroups({ ...record, grader_version: undefined, diagnostic: undefined }, t, () => false)).toEqual([])
  expect(iqEffortLabel('high')).toBe('high'); expect(iqEffortLabel('upstream_default')).toBe('default')
  const rows = iqTechnicalGroups({ ...record, diagnostic: { parser_version: 'future', stage: 'new-stage' } }, t, () => false).flatMap(group => group.rows)
  expect(rows.find(row => row.raw === 'new-stage')?.value).toBe('new-stage')
 })
})
