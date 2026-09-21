package adapter

import "time"

type Kind int

const (
	Unreachable Kind = iota
	Unauthorized
	Forbidden
	NotFound
	Conflict
	Invalid
	RateLimited
	NotSupported
	Upstream
)

// Error classifies an upstream failure without confusing upstream credentials
// with the panel user's authentication. Err retains the original cause.
type Error struct {
	Kind       Kind
	Op         string
	Resource   string
	Status     int
	Code       string
	RetryAfter time.Duration
	Err        error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return "upstream operation failed"
}

func (e *Error) Unwrap() error {
	return e.Err
}
