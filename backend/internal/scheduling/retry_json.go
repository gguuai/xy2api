package scheduling

import "encoding/json"

// UnmarshalJSON distinguishes omitted API fields from explicit false/zero.
// Go struct construction and NormalizePolicy keep their existing semantics.
func (p *RetryPolicy) UnmarshalJSON(raw []byte) error {
	type retryFields RetryPolicy
	next := retryFields(DefaultRetryPolicy())
	if err := json.Unmarshal(raw, &next); err != nil {
		return err
	}
	*p = RetryPolicy(next)
	return nil
}
