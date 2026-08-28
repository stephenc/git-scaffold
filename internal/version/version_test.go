package version

import "testing"

// TestStampedVersion covers the release path: the ldflags-stamped variable
// wins over anything the module system reports.
func TestStampedVersion(t *testing.T) {
	orig := version
	version = "1.2.3"
	t.Cleanup(func() { version = orig })
	if got := String(); got != "1.2.3" {
		t.Fatalf("String() = %q, want %q", got, "1.2.3")
	}
}

// TestUnstampedFallback covers the debug.ReadBuildInfo path. What it yields
// depends on how the test binary was built (a module version, a commit hash,
// or "unknown" when the build embeds no VCS data), but it must always be
// non-empty so `git-scaffold version` always prints something.
func TestUnstampedFallback(t *testing.T) {
	orig := version
	version = ""
	t.Cleanup(func() { version = orig })
	got := String()
	if got == "" {
		t.Fatal("String() = \"\", want a non-empty version")
	}
	if got == "(devel)" {
		t.Fatalf("String() = %q; the (devel) placeholder must never be reported", got)
	}
}
