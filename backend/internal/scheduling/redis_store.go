package scheduling

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisStore struct{ client redis.UniversalClient }

func NewRedisStore(client redis.UniversalClient) *RedisStore { return &RedisStore{client: client} }
func opaqueID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}
func digest(s string) string   { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:16]) }
func scopeKey(p Policy) string { return fmt.Sprintf("%d:%s", p.GroupID, p.Model) }
func profileKey(model string, p LatencyProfile, reasoning, bucket, transport string) string {
	raw, _ := json.Marshal([]string{"semantic_v4", strconv.FormatInt(p.HealthRevision, 10), model, p.Name, reasoning, bucket, transport, strconv.FormatInt(p.ContextMinTokens, 10), strconv.FormatInt(p.ContextMaxTokens, 10), strconv.FormatInt(p.HealthThresholdMS, 10), strconv.FormatInt(p.RecoveryThresholdMS, 10), strconv.FormatInt(p.AttemptTimeoutMS, 10)})
	return digest(string(raw))
}
func HealthRedisKey(accountID int64, model string, p LatencyProfile, reasoning, bucket, transport string, identity ...string) string {
	scope := profileKey(model, p, reasoning, bucket, transport)
	if len(identity) > 0 && identity[0] != "" {
		raw, _ := json.Marshal([]string{scope, identity[0]})
		scope = digest(string(raw))
	}
	return fmt.Sprintf("xy2:scheduling:health:{%d:%s}", accountID, scope)
}
func allocationKey(r SelectionRequest) string {
	kind := NormalizePolicy(r.Policy).Mode + ":initial"
	if r.Retry {
		kind = NormalizePolicy(r.Policy).Mode + ":retry"
	}
	if r.Policy.Mode == ModePin {
		kind += ":" + strconv.FormatInt(r.Policy.PinAccountID, 10)
	}
	return "xy2:scheduling:{" + digest(scopeKey(r.Policy)+":"+profileKey(r.Policy.Model, r.Profile, r.Reasoning, r.ContextBucket, r.Transport)+":"+kind) + "}"
}

func (s *RedisStore) Snapshots(ctx context.Context, r SelectionRequest) (map[int64]HealthSnapshot, error) {
	out := map[int64]HealthSnapshot{}
	if s == nil || s.client == nil {
		return nil, ErrSharedState
	}
	pipe := s.client.Pipeline()
	commands := map[int64]*redis.StringCmd{}
	for _, c := range r.Candidates {
		model := c.HealthModel
		if model == "" {
			model = r.Policy.Model
		}
		commands[c.AccountID] = pipe.HGet(ctx, HealthRedisKey(c.AccountID, model, r.Profile, r.Reasoning, r.ContextBucket, r.Transport, c.HealthIdentity), "snapshot")
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("%w: %v", ErrSharedState, err)
	}
	for id, cmd := range commands {
		v, e := cmd.Bytes()
		if errors.Is(e, redis.Nil) {
			continue
		}
		if e != nil {
			return nil, fmt.Errorf("%w: %v", ErrSharedState, e)
		}
		var state HealthSnapshot
		if e = json.Unmarshal(v, &state); e != nil {
			return nil, fmt.Errorf("%w: invalid health snapshot", ErrSharedState)
		}
		out[id] = state
	}
	return out, nil
}

const reserveSelectionLua = `
local score,res,expiry=KEYS[1],KEYS[2],KEYS[3]
local now,deadline=tonumber(ARGV[1]),tonumber(ARGV[2])
local id,sig=ARGV[3],ARGV[4]
local weights=cjson.decode(ARGV[5])
local function undo(raw)
 local old=cjson.decode(raw)
 if redis.call('HGET',score,'__sig')==old.sig then
  for _,v in ipairs(old.weights) do redis.call('HINCRBYFLOAT',score,tostring(v.id),-v.w) end
  redis.call('HINCRBYFLOAT',score,tostring(old.selected),old.total)
 end
end
local expired=redis.call('ZRANGEBYSCORE',expiry,'-inf',now,'LIMIT',0,100)
for _,rid in ipairs(expired) do local raw=redis.call('HGET',res,rid);if raw then undo(raw) end;redis.call('HDEL',res,rid);redis.call('ZREM',expiry,rid) end
-- Committed receipts may belong to dispatched requests: expiry only forgets.
local committed=redis.call('ZRANGEBYSCORE',KEYS[5],'-inf',now,'LIMIT',0,100)
for _,rid in ipairs(committed) do redis.call('HDEL',KEYS[4],rid);redis.call('ZREM',KEYS[5],rid) end
if redis.call('ZCARD',expiry)>=1024 then return redis.error_reply('too many pending reservations') end
if redis.call('HGET',score,'__sig')~=sig then redis.call('DEL',score);redis.call('HSET',score,'__sig',sig) end
local selected=nil;local best=nil;local total=0
for _,v in ipairs(weights) do
 local current=tonumber(redis.call('HINCRBYFLOAT',score,tostring(v.id),v.w));total=total+v.w
 if not best or current>best then best=current;selected=v.id end
end
if not selected or total<=0 then return redis.error_reply('empty allocation') end
redis.call('HINCRBYFLOAT',score,tostring(selected),-total)
redis.call('HSET',res,id,cjson.encode({sig=sig,weights=weights,total=total,selected=selected}))
redis.call('ZADD',expiry,deadline,id)
redis.call('EXPIRE',score,86400);redis.call('EXPIRE',res,86400);redis.call('EXPIRE',expiry,86400)
return tostring(selected)
`
const finalizeSelectionLua = `
local id,action,now=ARGV[1],ARGV[2],tonumber(ARGV[3])
local function erase()
 redis.call('HDEL',KEYS[2],id);redis.call('ZREM',KEYS[3],id)
 redis.call('HDEL',KEYS[4],id);redis.call('ZREM',KEYS[5],id)
end
if action=='forget' then erase();return 1 end
local raw=redis.call('HGET',KEYS[2],id)
local committed=false
local expiry=tonumber(redis.call('ZSCORE',KEYS[3],id) or '0')
if not raw then
 raw=redis.call('HGET',KEYS[4],id);committed=true
 expiry=tonumber(redis.call('ZSCORE',KEYS[5],id) or '0')
end
if not raw then return 0 end
local expired=expiry<=now
if action=='release' or (expired and not committed) then
 -- Never create make-up traffic from an expired committed receipt.
 if not (expired and committed) then
  local old=cjson.decode(raw)
  if redis.call('HGET',KEYS[1],'__sig')==old.sig then
   for _,v in ipairs(old.weights) do redis.call('HINCRBYFLOAT',KEYS[1],tostring(v.id),-v.w) end
   redis.call('HINCRBYFLOAT',KEYS[1],tostring(old.selected),old.total)
  end
 end
 erase()
 if expired and action=='commit' then return -1 end
 return 1
end
if expired then erase();return -1 end
if not committed then
 redis.call('HSET',KEYS[4],id,raw);redis.call('ZADD',KEYS[5],now+120000,id)
 redis.call('EXPIRE',KEYS[4],180);redis.call('EXPIRE',KEYS[5],180)
 redis.call('HDEL',KEYS[2],id);redis.call('ZREM',KEYS[3],id)
end
return 1
`

type allocationWeight struct {
	ID     int64
	Weight float64
}

func (s *RedisStore) reserve(ctx context.Context, key string, weights []allocationWeight, now time.Time) (int64, string, error) {
	if s == nil || s.client == nil {
		return 0, "", ErrSharedState
	}
	wire := make([]map[string]any, 0, len(weights))
	for _, w := range weights {
		wire = append(wire, map[string]any{"id": w.ID, "w": w.Weight})
	}
	raw, e := json.Marshal(wire)
	if e != nil {
		return 0, "", e
	}
	id := opaqueID()
	value, e := s.client.Eval(ctx, reserveSelectionLua, selectionRedisKeys(key), now.UnixMilli(), now.Add(30*time.Second).UnixMilli(), id, digest(string(raw)), string(raw)).Text()
	if e != nil {
		return 0, "", fmt.Errorf("%w: %v", ErrSharedState, e)
	}
	selected, e := strconv.ParseInt(value, 10, 64)
	return selected, id, e
}
func selectionRedisKeys(key string) []string {
	return []string{key + ":scores", key + ":reservations", key + ":expiry", key + ":receipts", key + ":receipt_expiry"}
}
func (s *RedisStore) finalize(ctx context.Context, d Decision, release bool) error {
	action := "commit"
	if release {
		action = "release"
	}
	return s.finalizeSelection(ctx, d, action)
}
func (s *RedisStore) finalizeSelection(ctx context.Context, d Decision, action string) error {
	if d.ReservationID == "" {
		return nil
	}
	if s == nil || s.client == nil {
		return ErrSharedState
	}
	n, e := s.client.Eval(ctx, finalizeSelectionLua, selectionRedisKeys(d.PoolKey), d.ReservationID, action, time.Now().UnixMilli()).Int()
	if e != nil {
		return fmt.Errorf("%w: %v", ErrSharedState, e)
	}
	if n < 0 || (action == "commit" && n == 0) {
		return fmt.Errorf("%w: selection reservation expired", ErrSharedState)
	}
	return nil
}

const acquireProbeLua = `
if redis.call('EXISTS',KEYS[1])==1 or redis.call('EXISTS',KEYS[2])==1 then return 0 end
redis.call('SET',KEYS[1],ARGV[1],'PX',ARGV[2]);redis.call('SET',KEYS[2],ARGV[1],'PX',ARGV[2]);return 1
`
const releaseProbeLua = `
for _,k in ipairs(KEYS) do if redis.call('GET',k)==ARGV[1] then redis.call('DEL',k) end end;return 1
`

func (s *RedisStore) acquireProbe(ctx context.Context, r SelectionRequest, accountID int64, sharedPool bool) (string, error) {
	key := "xy2:scheduling:probe:{" + profileKey(r.Policy.Model, r.Profile, r.Reasoning, r.ContextBucket, r.Transport) + "}"
	id := opaqueID()
	ttl := r.Profile.AttemptTimeoutMS + 10000
	if ttl < 60000 {
		ttl = 60000
	}
	keys := []string{key + ":pool", key + ":account:" + strconv.FormatInt(accountID, 10)}
	if !sharedPool {
		keys[0] = keys[1]
	}
	n, e := s.client.Eval(ctx, acquireProbeLua, keys, id, ttl).Int()
	if e != nil {
		return "", fmt.Errorf("%w: %v", ErrSharedState, e)
	}
	if n == 0 {
		return "", nil
	}
	return strings.Join(append(keys, id), "|"), nil
}
func (s *RedisStore) ReleaseProbe(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	parts := strings.Split(token, "|")
	if len(parts) != 3 {
		return fmt.Errorf("invalid probe token")
	}
	if s == nil || s.client == nil {
		return ErrSharedState
	}
	if e := s.client.Eval(ctx, releaseProbeLua, parts[:2], parts[2]).Err(); e != nil {
		return fmt.Errorf("%w: %v", ErrSharedState, e)
	}
	return nil
}

type BudgetReservation struct {
	ID      string
	PoolKey string
	Retry   bool
}

const acquireBudgetLua = `
local ratio,burst=tonumber(ARGV[1]),tonumber(ARGV[2]);local maximum=ratio*burst
local credit=math.min(maximum,tonumber(redis.call('GET',KEYS[1]) or tostring(maximum)));local delta=0;local owned=0
if ARGV[3]=='retry' then
 if credit<ratio then return 0 end;delta=-ratio
else
 if redis.call('SET',KEYS[3],ARGV[4],'NX','EX',86400) then owned=1 end
end
redis.call('SET',KEYS[1],credit+delta,'EX',86400)
redis.call('HSET',KEYS[2],'delta',delta,'owned',owned,'initial_key',KEYS[3],'maximum',maximum,'kind',ARGV[3],'state','reserved')
redis.call('EXPIRE',KEYS[2],120);return 1
`
const finalizeBudgetLua = `
if redis.call('EXISTS',KEYS[2])==0 then return 0 end
if ARGV[1]=='forget' then redis.call('DEL',KEYS[2]);return 1 end
local maximum=tonumber(redis.call('HGET',KEYS[2],'maximum') or '20')
if ARGV[1]=='commit' then
 if redis.call('HGET',KEYS[2],'state')=='committed' then return 1 end
 if redis.call('HGET',KEYS[2],'kind')=='initial' and redis.call('HGET',KEYS[2],'owned')=='1' then
  local credit=tonumber(redis.call('GET',KEYS[1]) or '0')
  local delta=math.max(0,math.min(1,maximum-credit))
  redis.call('SET',KEYS[1],credit+delta,'EX',86400)
  redis.call('HSET',KEYS[2],'delta',delta)
 end
 redis.call('HSET',KEYS[2],'state','committed');redis.call('EXPIRE',KEYS[2],120)
 return 1
end
if ARGV[1]=='refund' then
 local delta=tonumber(redis.call('HGET',KEYS[2],'delta') or '0');local credit=tonumber(redis.call('GET',KEYS[1]) or '0')
 redis.call('SET',KEYS[1],math.max(0,math.min(maximum,credit-delta)),'EX',86400)
 if redis.call('HGET',KEYS[2],'owned')=='1' then local k=redis.call('HGET',KEYS[2],'initial_key');if redis.call('GET',k)==ARGV[2] then redis.call('DEL',k) end end
end
redis.call('DEL',KEYS[2]);return 1
`

func (s *RedisStore) AcquireDispatchBudget(ctx context.Context, p Policy, logicalID string, retry bool) (BudgetReservation, error) {
	if s == nil || s.client == nil {
		return BudgetReservation{}, ErrSharedState
	}
	if logicalID == "" {
		return BudgetReservation{}, fmt.Errorf("logical request id is required")
	}
	p = NormalizePolicy(p)
	key := "xy2:scheduling:{" + digest(scopeKey(p)+":retry_budget") + "}"
	id := opaqueID()
	mode := "initial"
	if retry {
		mode = "retry"
	}
	n, e := s.client.Eval(ctx, acquireBudgetLua, []string{key + ":credit", key + ":budget:" + id, key + ":initial:" + digest(logicalID)}, p.Retry.InitialPerToken, p.Retry.Burst, mode, id).Int()
	if e != nil {
		return BudgetReservation{}, fmt.Errorf("%w: %v", ErrSharedState, e)
	}
	if n == 0 {
		return BudgetReservation{}, ErrRetryBudget
	}
	return BudgetReservation{ID: id, PoolKey: key, Retry: retry}, nil
}
func (s *RedisStore) finalizeBudget(ctx context.Context, b BudgetReservation, refund bool) error {
	mode := "commit"
	if refund {
		mode = "refund"
	}
	return s.finalizeBudgetReceipt(ctx, b, mode)
}
func (s *RedisStore) finalizeBudgetReceipt(ctx context.Context, b BudgetReservation, mode string) error {
	if b.ID == "" {
		return nil
	}
	if s == nil || s.client == nil {
		return ErrSharedState
	}
	n, e := s.client.Eval(ctx, finalizeBudgetLua, []string{b.PoolKey + ":credit", b.PoolKey + ":budget:" + b.ID}, mode, b.ID).Int()
	if e != nil {
		return fmt.Errorf("%w: %v", ErrSharedState, e)
	}
	if mode == "commit" && n == 0 {
		return fmt.Errorf("%w: budget reservation expired", ErrSharedState)
	}
	return nil
}
func (s *RedisStore) RefundDispatchBudget(ctx context.Context, b BudgetReservation) error {
	return s.finalizeBudget(ctx, b, true)
}
func (s *RedisStore) CommitDispatchBudget(ctx context.Context, b BudgetReservation) error {
	return s.finalizeBudget(ctx, b, false)
}

// CanRetry inspects shared credit without minting, reserving or consuming it.
// The actual dispatch must still acquire credit atomically because another
// request can consume the observed token before this request dispatches.
func (s *RedisStore) CanRetry(ctx context.Context, p Policy) (bool, error) {
	if s == nil || s.client == nil {
		return false, ErrSharedState
	}
	p = NormalizePolicy(p)
	key := "xy2:scheduling:{" + digest(scopeKey(p)+":retry_budget") + "}:credit"
	credit, err := s.client.Get(ctx, key).Int64()
	if errors.Is(err, redis.Nil) {
		return p.Retry.Burst > 0, nil
	}
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrSharedState, err)
	}
	return credit >= int64(p.Retry.InitialPerToken), nil
}

// ForgetDispatchReceipts confirms actual dispatch. Each call stays in its own
// Redis hash slot; clearing one receipt never refunds the other reservation.
func (s *RedisStore) ForgetDispatchReceipts(ctx context.Context, d Decision, b BudgetReservation) error {
	selectionErr := s.finalizeSelection(ctx, d, "forget")
	budgetErr := s.finalizeBudgetReceipt(ctx, b, "forget")
	return errors.Join(selectionErr, budgetErr)
}
