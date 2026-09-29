package service

import "sort"

// SchedulerAccountChangeBuckets includes group zero for simple-mode routing and
// the account's groups. Canonical buckets fence unpublished writers too.
// This never reopens retired groups or changes group permissions.
func SchedulerAccountChangeBuckets(accounts []*Account) []SchedulerBucket {
	groups := map[int64]bool{}
	for _, a := range accounts {
		if a == nil || a.ID <= 0 {
			continue
		}
		groups[0] = true
		for _, id := range a.GroupIDs {
			if id > 0 {
				groups[id] = true
			}
		}
	}
	ids := make([]int64, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	buckets := make([]SchedulerBucket, 0, len(ids)*schedulerCanonicalBucketCount())
	for _, id := range ids {
		buckets = append(buckets, schedulerCanonicalBuckets(id)...)
	}
	return buckets
}
