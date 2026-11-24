package cli

import (
	"runtime/debug"
	"testing"
)

// A release binary must report the version its build stamped in, and a
// binary installed with `go install pkg@version` — which cannot carry a
// stamp — must not fall back to "dev" while Go recorded a module version.
func TestBuildVersionPrefersTheStampThenTheRecordedModuleVersion(t *testing.T) {
	stamped := Version
	t.Cleanup(func() { Version = stamped })

	Version = "v9.9.9"
	if got := BuildVersion(); got != "v9.9.9" {
		t.Errorf("BuildVersion() with Version stamped = %q, want v9.9.9", got)
	}

	Version = "dev"
	got := BuildVersion()
	info, ok := debug.ReadBuildInfo()
	recorded := ""
	if ok {
		recorded = info.Main.Version
	}
	want := "dev" // a test binary is built from a checkout, which records "(devel)"
	if recorded != "" && recorded != "(devel)" {
		want = recorded
	}
	if got != want {
		t.Errorf("BuildVersion() unstamped = %q, want %q (module version on record: %q)", got, want, recorded)
	}
}
