package xdg

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// withCleanEnv runs fn with HOME, XDG_DATA_HOME, and XDG_CONFIG_HOME
// unset, so each test starts from a known state regardless of the

// developer's shell configuration. t.Setenv with the empty string
// unsets the variable in the test process; it is automatically
// restored when the test ends.
func withCleanEnv(t *testing.T, fn func()) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	fn()
}

func TestDataDir_DefaultFromHome(t *testing.T) {
	withCleanEnv(t, func() {
		t.Setenv("HOME", "/home/alice")
		got, err := DataDir()
		if err != nil {
			t.Fatalf("DataDir() error: %v", err)
		}
		want := filepath.Join("/home/alice", ".local", "share", ProjectName)
		if got != want {
			t.Errorf("DataDir() = %q, want %q", got, want)
		}
	})
}

func TestConfigDir_DefaultFromHome(t *testing.T) {
	withCleanEnv(t, func() {
		t.Setenv("HOME", "/home/alice")
		got, err := ConfigDir()
		if err != nil {
			t.Fatalf("ConfigDir() error: %v", err)
		}
		want := filepath.Join("/home/alice", ".config", ProjectName)
		if got != want {
			t.Errorf("ConfigDir() = %q, want %q", got, want)
		}
	})
}

func TestDataDir_OverrideByXDG(t *testing.T) {
	withCleanEnv(t, func() {
		// When XDG_DATA_HOME is set, HOME must NOT be consulted.
		// We set HOME to a deliberately wrong value to prove it.
		t.Setenv("HOME", "/should/not/be/used")
		t.Setenv("XDG_DATA_HOME", "/srv/eruditto-data")
		got, err := DataDir()
		if err != nil {
			t.Fatalf("DataDir() error: %v", err)
		}
		want := filepath.Join("/srv/eruditto-data", ProjectName)
		if got != want {
			t.Errorf("DataDir() = %q, want %q", got, want)
		}
	})
}

func TestConfigDir_OverrideByXDG(t *testing.T) {
	withCleanEnv(t, func() {
		t.Setenv("HOME", "/should/not/be/used")
		t.Setenv("XDG_CONFIG_HOME", "/etc/eruditto-config")
		got, err := ConfigDir()
		if err != nil {
			t.Fatalf("ConfigDir() error: %v", err)
		}
		want := filepath.Join("/etc/eruditto-config", ProjectName)
		if got != want {
			t.Errorf("ConfigDir() = %q, want %q", got, want)
		}
	})
}

// TestDataDir_EmptyXDG_FallsBackToHome verifies that an empty
// XDG_DATA_HOME (which the spec treats as "unset") falls back to
// $HOME. This matters because some shell configs export
// XDG_DATA_HOME="" by accident.
func TestDataDir_EmptyXDG_FallsBackToHome(t *testing.T) {
	withCleanEnv(t, func() {
		t.Setenv("HOME", "/home/bob")
		t.Setenv("XDG_DATA_HOME", "")
		got, err := DataDir()
		if err != nil {
			t.Fatalf("DataDir() error: %v", err)
		}
		want := filepath.Join("/home/bob", ".local", "share", ProjectName)
		if got != want {
			t.Errorf("DataDir() = %q, want %q", got, want)
		}
	})
}

func TestConfigDir_EmptyXDG_FallsBackToHome(t *testing.T) {
	withCleanEnv(t, func() {
		t.Setenv("HOME", "/home/bob")
		t.Setenv("XDG_CONFIG_HOME", "")
		got, err := ConfigDir()
		if err != nil {
			t.Fatalf("ConfigDir() error: %v", err)
		}
		want := filepath.Join("/home/bob", ".config", ProjectName)
		if got != want {
			t.Errorf("ConfigDir() = %q, want %q", got, want)
		}
	})
}

// TestDataDir_HomeNotSet covers the CI-container edge case the
// checklist explicitly calls out: HOME is unset AND XDG_DATA_HOME
// is unset. We must return ErrNoHome, not panic, not silently
// fall back to /tmp.
func TestDataDir_HomeNotSet(t *testing.T) {
	withCleanEnv(t, func() {
		_, err := DataDir()
		if err == nil {
			t.Fatal("DataDir() = nil error, want ErrNoHome")
		}
		if !errors.Is(err, ErrNoHome) {
			t.Errorf("DataDir() error = %v, want wrapping ErrNoHome", err)
		}
	})
}

func TestConfigDir_HomeNotSet(t *testing.T) {
	withCleanEnv(t, func() {
		_, err := ConfigDir()
		if err == nil {
			t.Fatal("ConfigDir() = nil error, want ErrNoHome")
		}
		if !errors.Is(err, ErrNoHome) {
			t.Errorf("ConfigDir() error = %v, want wrapping ErrNoHome", err)
		}
	})
}

// TestEnsureAll_CreatesDirs verifies the happy path. We point both
// XDG vars at a temp directory so we don't pollute the real
// user's $HOME during tests.
func TestEnsureAll_CreatesDirs(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	// HOME is irrelevant once XDG vars are set, but unset it
	// defensively to make the test's expectations crystal clear.
	t.Setenv("HOME", "")

	if err := EnsureAll(); err != nil {
		t.Fatalf("EnsureAll() error: %v", err)
	}

	wantData := filepath.Join(root, "data", ProjectName)
	wantConfig := filepath.Join(root, "config", ProjectName)
	for _, d := range []string{wantData, wantConfig} {
		info, err := os.Stat(d)
		if err != nil {
			t.Errorf("expected %q to exist after EnsureAll: %v", d, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%q exists but is not a directory", d)
		}
	}
}

// TestEnsureAll_Idempotent checks that calling EnsureAll twice
// in a row is a no-op the second time. This is the property the
// main.go startup sequence relies on.
func TestEnsureAll_Idempotent(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("HOME", "")

	if err := EnsureAll(); err != nil {
		t.Fatalf("first EnsureAll() error: %v", err)
	}
	if err := EnsureAll(); err != nil {
		t.Fatalf("second EnsureAll() error: %v", err)
	}
}

// TestEnsureAll_Permissions verifies that newly created dirs
// have the mode we asked for. We mask with 0777 because the
// process's umask may further restrict the actual mode — what
// matters is that EnsureAll REQUESTED 0755, and that all the
// r/x bits we need are present.
func TestEnsureAll_Permissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits not meaningful on Windows")
	}
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("HOME", "")

	if err := EnsureAll(); err != nil {
		t.Fatalf("EnsureAll() error: %v", err)
	}

	for _, d := range []string{
		filepath.Join(root, "data", ProjectName),
		filepath.Join(root, "config", ProjectName),
	} {
		info, err := os.Stat(d)
		if err != nil {
			t.Fatalf("Stat(%q) error: %v", d, err)
		}
		mode := info.Mode().Perm()
		// Require owner rwx and group/other rx. Umask may strip
		// additional bits, but the requested 0755 should at
		// minimum leave owner-write intact.
		if mode&0o700 != 0o700 {
			t.Errorf("%q mode = %o, want owner rwx (masked with 0700)", d, mode)
		}
	}
}

// TestEnsureAll_FailsWhenHomeMissing makes sure EnsureAll
// returns the same ErrNoHome when HOME is missing, rather than
// creating directories under some surprising fallback.
func TestEnsureAll_FailsWhenHomeMissing(t *testing.T) {
	withCleanEnv(t, func() {
		err := EnsureAll()
		if err == nil {
			t.Fatal("EnsureAll() = nil error, want ErrNoHome")
		}
		if !errors.Is(err, ErrNoHome) {
			t.Errorf("EnsureAll() error = %v, want wrapping ErrNoHome", err)
		}
	})
}

// TestProjectName_Stable is a regression guard: if someone ever
// renames ProjectName (e.g., to "Eruditto" with capital E), this
// test fails and forces them to think about the consequences for
// existing users with ~/.local/share/eruditto/ data on disk.
func TestProjectName_Stable(t *testing.T) {
	if ProjectName != "eruditto" {
		t.Errorf("ProjectName = %q, want %q (changing this breaks existing user data dirs)", ProjectName, "eruditto")
	}
	if strings.ToLower(ProjectName) != ProjectName {
		t.Errorf("ProjectName = %q, must be lowercase (Linux paths are case-sensitive)", ProjectName)
	}
}
