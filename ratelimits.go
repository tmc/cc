package cc

// NewestRateLimits returns the latest timestamped rate-limit sample, or nil
// when none is present. On equal timestamps, the last entry wins. Undated
// samples precede timestamped samples; an all-undated stream uses its last
// sample. The returned pointer belongs to the supplied entries.
func NewestRateLimits(entries []Entry) *RateLimits {
	var newest *Entry
	for i := range entries {
		e := &entries[i]
		if e.RateLimits != nil && (newest == nil || !e.Timestamp.Before(newest.Timestamp)) {
			newest = e
		}
	}
	if newest == nil {
		return nil
	}
	return newest.RateLimits
}
