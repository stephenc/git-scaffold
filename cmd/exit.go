package cmd

import "errors"

// ExitError carries a specific process exit status. `check` and `outdated`
// use distinct statuses to report their answer, per the design specification.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err == nil {
		return "exit"
	}
	return e.Err.Error()
}

func (e *ExitError) Unwrap() error { return e.Err }

func exitCode(err error) int {
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return 1
}
