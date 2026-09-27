-- Distinguish current health from the last valid candy result.
ALTER TABLE account_iq_check_attempts DROP CONSTRAINT account_iq_check_attempts_attempt_no_check;
ALTER TABLE account_iq_check_attempts ADD CONSTRAINT account_iq_check_attempts_attempt_no_check CHECK (attempt_no BETWEEN 1 AND 3);
UPDATE accounts SET iq_check = iq_check || jsonb_build_object(
 'last_valid_status', CASE WHEN iq_check->>'status' IN ('smart','degraded') THEN iq_check->>'status' ELSE NULL END,
 'last_valid_reason', CASE WHEN iq_check->>'status' IN ('smart','degraded') THEN iq_check->>'reason' ELSE NULL END,
 'status', CASE WHEN iq_check->>'last_run_status'='unknown' THEN 'unknown' ELSE COALESCE(iq_check->>'status','unknown') END,
 'reason', CASE WHEN iq_check->>'last_run_status'='unknown' THEN iq_check->>'last_run_reason' ELSE iq_check->>'reason' END
) WHERE iq_check->>'enabled'='true';

CREATE OR REPLACE FUNCTION xy_iq_apply_settings(original JSONB, patch JSONB, identity_changed BOOLEAN, at_time TIMESTAMPTZ)
RETURNS JSONB LANGUAGE plpgsql AS $$
DECLARE
 prior JSONB := '{"enabled":false,"interval_minutes":5,"model":"gpt-6-astra","reasoning_effort":"low","output_mode":"compat","status":"unknown","timeout_seconds":120}'::jsonb || COALESCE(original,'{}'::jsonb);
 result JSONB;
 reset_result BOOLEAN;
 next_time TIMESTAMPTZ;
BEGIN
 result := (prior || COALESCE((SELECT jsonb_object_agg(key,value) FROM jsonb_each(COALESCE(patch,'{}'::jsonb)) WHERE key IN ('enabled','interval_minutes','timeout_seconds','model','reasoning_effort','output_mode')),'{}'::jsonb)) - ARRAY['scheduling_mode','max_interval_minutes','daily_request_limit','quota_group','budget_day','budget_used','budget_remaining','budget_warning','smart_streak']::text[];
 reset_result := identity_changed OR (prior->'enabled' IS DISTINCT FROM result->'enabled')
  OR (prior->'model' IS DISTINCT FROM result->'model')
  OR (prior->'reasoning_effort' IS DISTINCT FROM result->'reasoning_effort')
  OR (prior->'output_mode' IS DISTINCT FROM result->'output_mode')
  OR (prior->'timeout_seconds' IS DISTINCT FROM result->'timeout_seconds');
 IF reset_result THEN
  result := result || jsonb_build_object('status','unknown','reason',CASE WHEN result->>'enabled'='true' THEN 'configuration_changed' ELSE '' END,
   'last_valid_at',NULL,'last_valid_status','','last_valid_reason','','scheduling_blocked',false,'last_run_status','','last_run_reason','','round_id','','round_deadline',NULL,'retry_at',NULL,'attempt_count',0,'busy_deferrals',0,'failure_streak',0,'protocol_failures',0,'execution_state','idle','execution_reason','','task_id','',
   'revision',md5(random()::text || clock_timestamp()::text),'next_run_at',NULL);
 END IF;
 IF result->>'enabled'='true' AND (reset_result OR (COALESCE(result->>'execution_state','') <> 'paused' AND
  (prior->'interval_minutes' IS DISTINCT FROM result->'interval_minutes') AND COALESCE((result->>'lease_until')::timestamptz,'-infinity')<=at_time)) THEN
  next_time:=GREATEST(at_time,COALESCE((result->>'last_run_at')::timestamptz+make_interval(mins=>(result->>'interval_minutes')::int),at_time),COALESCE((result->>'last_attempt_at')::timestamptz+make_interval(mins=>(result->>'interval_minutes')::int),at_time),COALESCE((result->>'not_before')::timestamptz,at_time));
  result:=result||jsonb_build_object('next_run_at',next_time,'execution_state',CASE WHEN next_time>at_time THEN 'deferred' ELSE 'pending' END,
   'execution_reason',CASE WHEN next_time>at_time THEN 'minimum_interval' ELSE '' END,'task_id',md5(random()::text||clock_timestamp()::text));
 END IF;
 IF NOT reset_result AND prior->>'retry_at' IS NOT NULL THEN result:=result||jsonb_build_object('task_id',prior->'task_id','next_run_at',prior->'next_run_at','execution_state',prior->'execution_state','execution_reason',prior->'execution_reason'); END IF;
 RETURN result;
END $$;

-- Resume historical depleted-balance pauses at a bounded recovery cadence.
UPDATE accounts SET iq_check=iq_check||jsonb_build_object(
 'execution_state','deferred','execution_reason','quota_exhausted',
 'next_run_at',GREATEST(now()+interval '15 minutes',COALESCE((iq_check->>'not_before')::timestamptz,now())),
 'task_id',md5(random()::text||clock_timestamp()::text))
WHERE deleted_at IS NULL AND iq_check->>'enabled'='true'
 AND iq_check->>'execution_state'='paused' AND iq_check->>'execution_reason'='quota_exhausted'
 AND COALESCE((iq_check->>'lease_until')::timestamptz,'-infinity')<=now();
