package vault

import "time"

// SetDefaultTimeout sets the default client's request timeout and returns a
// function that restores the previous value.
func SetDefaultTimeout(d time.Duration) (restore func()) {
	previous := defaultTimeout
	defaultTimeout = d
	return func() { defaultTimeout = previous }
}
