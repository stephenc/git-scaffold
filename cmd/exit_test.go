package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestExitErrorMessage(t *testing.T) {
	inner := errors.New("boom")
	wrapped := &ExitError{Code: 2, Err: inner}
	if got := wrapped.Error(); got != "boom" {
		t.Fatalf("Error() = %q, want %q", got, "boom")
	}
	if got := errors.Unwrap(wrapped); got != inner {
		t.Fatalf("Unwrap() = %v, want %v", got, inner)
	}

	statusOnly := &ExitError{Code: 1}
	if got := statusOnly.Error(); got != "exit" {
		t.Fatalf("status-only Error() = %q, want %q", got, "exit")
	}
	if got := errors.Unwrap(statusOnly); got != nil {
		t.Fatalf("status-only Unwrap() = %v, want nil", got)
	}
}

func TestExitCode(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"plain error":        {errors.New("boom"), 1},
		"exit error":         {&ExitError{Code: 3, Err: errors.New("boom")}, 3},
		"status-only":        {&ExitError{Code: 2}, 2},
		"wrapped exit error": {fmt.Errorf("context: %w", &ExitError{Code: 4, Err: errors.New("boom")}), 4},
	} {
		if got := exitCode(tc.err); got != tc.want {
			t.Errorf("%s: exitCode = %d, want %d", name, got, tc.want)
		}
	}
}

// TestFinish covers the Execute error reporting: a plain error prints the
// bold error line and exits 1; an ExitError with a wrapped error prints its
// message and exits with its code; a status-only ExitError is silent (the
// command already printed its answer to stdout).
func TestFinish(t *testing.T) {
	for name, tc := range map[string]struct {
		err         error
		wantCode    int
		wantPrinted bool
		wantOutput  string
	}{
		"success":        {nil, 0, false, ""},
		"plain error":    {errors.New("boom"), 1, true, "boom"},
		"exit error":     {&ExitError{Code: 2, Err: errors.New("cannot fetch")}, 2, true, "cannot fetch"},
		"status-only":    {&ExitError{Code: 1}, 1, false, ""},
		"status-only 20": {&ExitError{Code: 20}, 20, false, ""},
	} {
		var errw bytes.Buffer
		code, printed := finish(tc.err, &errw)
		if code != tc.wantCode || printed != tc.wantPrinted {
			t.Errorf("%s: finish = (%d, %v), want (%d, %v)", name, code, printed, tc.wantCode, tc.wantPrinted)
		}
		out := errw.String()
		if !tc.wantPrinted {
			if out != "" {
				t.Errorf("%s: unexpected output %q", name, out)
			}
			continue
		}
		if !strings.Contains(out, "error:") || !strings.Contains(out, tc.wantOutput) {
			t.Errorf("%s: output %q missing error line with %q", name, out, tc.wantOutput)
		}
	}
}
