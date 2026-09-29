package scheduling

// admissionOccupancySQL is shared by candidate reads, transactional capacity
// admission, and recovery probes. Expiry releases only a local scheduling hold;
// it never asserts that a provider finished or removes an unknown usage record.
// The prefix is a literal SQL table alias supplied exclusively by this package.
func admissionOccupancySQL(prefix string) string {
	return "((" + prefix + "state='dispatched' AND " + prefix + "lease_until>NOW()) OR (" + prefix + "state='unknown' AND " + prefix + "unknown_hold_until>NOW()))"
}
