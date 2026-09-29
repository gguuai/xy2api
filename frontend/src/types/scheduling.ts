/** Duration wire fields are milliseconds unless explicitly named otherwise. */
export interface GroupSchedulingAccount {
  account_id: number
  priority: number
  traffic_weight: number
}
export interface GroupSchedulingPolicy {
  group_id: number
  version: number
  accounts: GroupSchedulingAccount[]
  first_output_timeout_ms: number
  total_wait_timeout_ms: number
  max_attempts: number
}
export interface GroupSchedulingMigrationWarning {
  code: string
  message: string
  models?: string[]
  account_ids?: number[]
}
export interface GroupSchedulingDocument {
  policy: GroupSchedulingPolicy
  version: number
  group_id?: number
  configured?: boolean
  default_scope?: 'group' | 'ungrouped' | 'all_accounts'
  migration_warnings?: GroupSchedulingMigrationWarning[]
}
