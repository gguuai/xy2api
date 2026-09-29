package scheduling

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const healthRecordTTL = 24 * time.Hour

// One bounded hash stores the state and deduplication receipts. On receipt
// saturation only the incarnation rotates; established health is preserved.
const healthReceiptLimit int64 = 4096

// FreezeHealth initializes the observation incarnation if necessary and freezes
// the recovery stage that admits this dispatch. It changes no PG control or
// attempt capacity and cannot release an uncertain upstream attempt.
func (r *Runtime) FreezeHealth(ctx context.Context, accountID int64, model string, p LatencyProfile, reasoning, bucket, transport string, identity ...string) (HealthFence, error) {
	if r == nil || r.store == nil {
		return HealthFence{}, ErrSharedState
	}
	stable := ""
	if len(identity) > 0 {
		stable = identity[0]
	}
	return r.store.freezeHealth(ctx, accountID, model, p, reasoning, bucket, transport, stable, nil)
}

// FreezeSelectedHealth is the final Redis health admission point before PG
// BeginDispatch. A later OPEN applies to work still waiting for admission; work
// that passed this point is already admitted and its frozen stage remains valid.
// It does not claim an atomic transaction across Redis and PostgreSQL.
func (r *Runtime) FreezeSelectedHealth(ctx context.Context, accountID int64, model string, p LatencyProfile, reasoning, bucket, transport, identity string, selected Decision) (HealthFence, error) {
	if r == nil || r.store == nil {
		return HealthFence{}, ErrSharedState
	}
	if selected.AccountID != accountID {
		return HealthFence{}, ErrHealthSelectionStale
	}
	if p.Name == AccountPoolProfileName && selected.HealthFence == nil {
		return HealthFence{}, ErrHealthSelectionStale
	}
	return r.store.freezeHealth(ctx, accountID, model, p, reasoning, bucket, transport, identity, &selected)
}

func (s *RedisStore) freezeHealth(ctx context.Context, accountID int64, model string, p LatencyProfile, reasoning, bucket, transport, identity string, selected *Decision) (HealthFence, error) {
	if s == nil || s.client == nil {
		return HealthFence{}, ErrSharedState
	}
	if accountID <= 0 || model == "" || p.HealthRevision < 0 {
		return HealthFence{}, ErrHealthIdentity
	}
	key := HealthRedisKey(accountID, model, p, reasoning, bucket, transport, identity)
	watchKeys := []string{key}
	probeKey := ""
	if p.Name == AccountPoolProfileName && selected != nil {
		probeKey = accountPoolProbePrefix(accountID, model, p, reasoning, bucket, transport, identity) + ":account:" + strconv.FormatInt(accountID, 10)
		watchKeys = append(watchKeys, probeKey)
	}
	var fence HealthFence
	for n := 0; n < 8; n++ {
		err := s.client.Watch(ctx, func(tx *redis.Tx) error {
			var old HealthSnapshot
			b, err := tx.HGet(ctx, key, "snapshot").Bytes()
			missing := errors.Is(err, redis.Nil)
			if err != nil && !missing {
				return err
			}
			if !missing {
				if err = json.Unmarshal(b, &old); err != nil {
					return err
				}
			}
			next := FreshHealth(old, time.Now(), p)
			if selected != nil {
				if next.State == HealthOpen {
					return ErrHealthSelectionStale
				}
				if expected := selected.HealthFence; expected != nil {
					if expected.Model != model || expected.HealthIdentity != identity || expected.HealthRevision != p.HealthRevision || expected.Generation != old.Generation || expected.StageRevision != next.StageRevision || expected.State != next.State {
						return ErrHealthSelectionStale
					}
				}
				if probeKey != "" && next.State == HealthHalfOpen {
					parts := strings.Split(selected.ProbeToken, "|")
					if !selected.Probe || len(parts) != 3 || parts[0] != probeKey || parts[1] != probeKey || parts[2] == "" {
						return ErrHealthSelectionStale
					}
					owner, err := tx.Get(ctx, probeKey).Result()
					if errors.Is(err, redis.Nil) || (err == nil && owner != parts[2]) {
						return ErrHealthSelectionStale
					}
					if err != nil {
						return err
					}
				}
			}
			if next.Generation == "" {
				next.Generation = opaqueID()
			}
			fence = HealthFence{Model: model, Generation: next.Generation, StageRevision: next.StageRevision, HealthRevision: p.HealthRevision, HealthIdentity: identity, State: next.State}
			if !missing && next.Generation == old.Generation && next.State == old.State && next.StageRevision == old.StageRevision && next.RecoveryRequirement == old.RecoveryRequirement {
				if probeKey == "" {
					return nil
				}
				// EXEC a read-only command even without a health transition. WATCH
				// otherwise would not validate the joint generation/lease snapshot.
				_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
					pipe.Exists(ctx, key)
					return nil
				})
				return err
			}
			raw, err := json.Marshal(next)
			if err != nil {
				return err
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.HSet(ctx, key, "snapshot", raw)
				if missing {
					pipe.Expire(ctx, key, healthRecordTTL)
				}
				return nil
			})
			return err
		}, watchKeys...)
		if err == nil {
			return fence, nil
		}
		if errors.Is(err, ErrHealthSelectionStale) {
			return HealthFence{}, err
		}
		if !errors.Is(err, redis.TxFailedErr) {
			return HealthFence{}, fmt.Errorf("%w: %v", ErrSharedState, err)
		}
	}
	return HealthFence{}, fmt.Errorf("%w: concurrent health freeze exceeded retry bound", ErrSharedState)
}

// Observe atomically records one terminal observation per attempt. The receipt
// and snapshot occupy one Redis hash, so key eviction cannot lose only dedup.
// Duplicate callbacks do not extend TTL; record loss rejects old generations.
// Bounded generation rotation preserves health but discards old in-flight
// feedback conservatively; new dispatches must freeze the new incarnation.
func (s *RedisStore) Observe(ctx context.Context, o Observation) error {
	if o.Excluded || (!o.Completed && !o.AttributableFailure && !o.FirstOutputTimeout) {
		return nil
	}
	if s == nil || s.client == nil {
		return ErrSharedState
	}
	if o.AccountID <= 0 || o.Model == "" || o.AttemptID == "" || o.Fence == nil || o.Fence.Generation == "" || o.Fence.HealthRevision != o.Profile.HealthRevision || o.Fence.HealthIdentity != o.HealthIdentity || o.Fence.Model != o.Model {
		return ErrHealthIdentity
	}
	if o.At.IsZero() {
		o.At = time.Now()
	}
	// Receipts have the same retention horizon as health records. An immutable
	// terminal observation older than that horizon is never counted again.
	if time.Since(o.At) >= healthRecordTTL {
		return nil
	}
	key := HealthRedisKey(o.AccountID, o.Model, o.Profile, o.Reasoning, o.ContextBucket, o.Transport, o.HealthIdentity)
	receipt := "observed:" + digest(o.AttemptID)
	for n := 0; n < 8; n++ {
		err := s.client.Watch(ctx, func(tx *redis.Tx) error {
			seen, err := tx.HExists(ctx, key, receipt).Result()
			if err != nil || seen {
				return err
			}
			var old HealthSnapshot
			b, err := tx.HGet(ctx, key, "snapshot").Bytes()
			if errors.Is(err, redis.Nil) {
				return nil // expired/evicted health is re-created only by new dispatch
			}
			if err != nil {
				return err
			}
			if err = json.Unmarshal(b, &old); err != nil {
				return err
			}
			if old.Generation != o.Fence.Generation {
				return nil
			}
			size, err := tx.HLen(ctx, key).Result()
			if err != nil {
				return err
			}
			next := AdvanceHealth(old, o)
			rotate := size >= healthReceiptLimit+1
			if rotate {
				next.Generation = opaqueID()
			}
			raw, err := json.Marshal(next)
			if err != nil {
				return err
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				if rotate {
					pipe.Del(ctx, key)
				}
				pipe.HSet(ctx, key, "snapshot", raw, receipt, o.At.UnixMilli())
				pipe.Expire(ctx, key, healthRecordTTL)
				return nil
			})
			return err
		}, key)
		if err == nil {
			return nil
		}
		if !errors.Is(err, redis.TxFailedErr) {
			return fmt.Errorf("%w: %v", ErrSharedState, err)
		}
	}
	return fmt.Errorf("%w: concurrent health updates exceeded retry bound", ErrSharedState)
}
