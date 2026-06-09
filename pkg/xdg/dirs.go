// Package xdg provides XDG Base Directory Specification-compliant
// paths for Eruditto's data and config directories.
//
// The package follows the spec at:
//
//	https://specifications.freedesktop.org/basedir-spec/basedir-spec-latest.html
//
// Conventions for Eruditto:
//   - Data dir:   $XDG_DATA_HOME/eruditto    (default: ~/.local/share/eruditto)
//   - Config dir: $XDG_CONFIG_HOME/eruditto  (default: ~/.config/eruditto)
//
// DataDir and ConfigDir compute paths without touching the
// filesystem. Use EnsureAll to actually create the directories.
package xdg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ProjectName is the directory name Eruditto uses inside both
// $XDG_DATA_HOME and $XDG_CONFIG_HOME. Centralized as a constant
// so it can be referenced in tests and elsewhere without
// scattering string literals.
const ProjectName = "eruditto"

// DirPerm is the permission mode used when EnsureAll creates the
// data and config directories. 0755 means owner read/write/execute,
// group/others read/execute — standard for user data dirs on
// multi-user systems.
const DirPerm = 0o755

// ErrNoHome is returned when neither XDG_DATA_HOME/XDG_CONFIG_HOME
// nor HOME is set in the environment. The XDG spec assumes HOME
// exists; if it doesn't, the caller must decide whether to fall
// back to a temporary location, log and continue, or fail.
var ErrNoHome = errors.New("xdg: HOME is not set and no XDG override provided")

// dataHome returns $XDG_DATA_HOME if set and non-empty, otherwise
// $HOME/.local/share, otherwise an error.
func dataHome() (string, error) {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return v, nil
	}
	home, err := unixHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share"), nil
}

// configHome returns $XDG_CONFIG_HOME if set and non-empty,
// otherwise $HOME/.config, otherwise an error.
func configHome() (string, error) {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v, nil
	}
	home, err := unixHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config"), nil
}

// unixHome returns $HOME, or an error if it is unset or empty.
// The empty-string check is important because some shells and
// test setups export HOME="" which is semantically equivalent
// to HOME being unset.
func unixHome() (string, error) {
	v := os.Getenv("HOME")
	if v == "" {
		return "", ErrNoHome
	}
	return v, nil
}

// DataDir returns the absolute path to Eruditto's data directory,
// without creating it on disk. Call EnsureAll to create it.
func DataDir() (string, error) {
	base, err := dataHome()
	if err != nil {
		return "", fmt.Errorf("xdg: data dir: %w", err)
	}
	return filepath.Join(base, ProjectName), nil
}

// ConfigDir returns the absolute path to Eruditto's config
// directory, without creating it on disk. Call EnsureAll to
// create it.
func ConfigDir() (string, error) {
	base, err := configHome()
	if err != nil {
		return "", fmt.Errorf("xdg: config dir: %w", err)
	}
	return filepath.Join(base, ProjectName), nil
}

// EnsureAll creates the data and config directories (and any
// missing parents) if they do not already exist. The function is
// idempotent: calling it on an already-initialized system is a
// no-op. Permissions are 0755 (see DirPerm).
//
// If either directory cannot be created, the function returns
// the first error encountered. We do NOT roll back a partial

// creation: a half-created tree is a more useful failure mode
// than a clean non-existent tree, because the user can inspect
// what was attempted.
func EnsureAll() error {
	dirs := []string{}
	data, err := DataDir()
	if err != nil {
		return err
	}
	dirs = append(dirs, data)

	config, err := ConfigDir()
	if err != nil {
		return err
	}
	dirs = append(dirs, config)

	for _, d := range dirs {
		// MkdirAll is idempotent: returns nil if the dir already
		// exists. We deliberately don't os.Stat first because
		// that introduces a TOCTOU race that MkdirAll avoids.
		if err := os.MkdirAll(d, DirPerm); err != nil {
			return fmt.Errorf("xdg: mkdir %q: %w", d, err)
		}
	}
	return nil
}
