package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

var ErrDesktopAuthNotFound = errors.New("no CodeBuddy desktop auth files found")

// DesktopAuthDirs returns the platform-specific locations used by the
// CodeBuddy/WorkBuddy desktop extension. CODEBUDDY_AUTH_DIR is intended for
// portable installs and tests and takes precedence over platform defaults.
func DesktopAuthDirs() []string {
	if override := strings.TrimSpace(os.Getenv("CODEBUDDY_AUTH_DIR")); override != "" {
		return []string{override}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	switch runtime.GOOS {
	case "windows":
		base := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
		if base == "" {
			base = filepath.Join(home, "AppData", "Local")
		}
		return desktopAuthProductDirs(base)
	case "darwin":
		return desktopAuthProductDirs(filepath.Join(home, "Library", "Application Support"))
	default:
		base := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
		if base == "" {
			base = filepath.Join(home, ".local", "share")
		}
		return desktopAuthProductDirs(base)
	}
}

func desktopAuthProductDirs(base string) []string {
	return []string{
		filepath.Join(base, "CodeBuddyExtension", "Data", "Public", "auth"),
		filepath.Join(base, "WorkBuddyExtension", "Data", "Public", "auth"),
	}
}

// LoadDesktopAuthAccounts reads existing desktop auth snapshots but does not
// modify them. The gateway imports the resulting credentials into its own DB,
// which remains the sole runtime source of truth.
func LoadDesktopAuthAccounts(path string) ([]ImportedAccount, error) {
	dirs := DesktopAuthDirs()
	if strings.TrimSpace(path) != "" {
		dirs = []string{path}
	}
	var matches []string
	for _, dir := range dirs {
		files, err := filepath.Glob(filepath.Join(dir, "*.info"))
		if err != nil {
			return nil, err
		}
		matches = append(matches, files...)
	}
	if len(matches) == 0 {
		return nil, ErrDesktopAuthNotFound
	}
	sort.Strings(matches)
	var all []ImportedAccount
	for _, file := range matches {
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read desktop auth %s: %w", file, err)
		}
		items, err := ParseImportedJSON(raw)
		if err != nil {
			return nil, fmt.Errorf("parse desktop auth %s: %w", file, err)
		}
		all = append(all, items...)
	}
	all = dedupeImported(all)
	if len(all) == 0 {
		return nil, ErrDesktopAuthNotFound
	}
	return all, nil
}

// ImportDesktopAccounts is a small service-level convenience for CLI and the
// Windows double-click bootstrap. It imports into SQLite and never writes the
// original desktop auth files.
func ImportDesktopAccounts(items []ImportedAccount) (created, updated int, err error) {
	for _, item := range items {
		acc, isNew, err := UpsertAccount(item.ToModel())
		if err != nil {
			return created, updated, err
		}
		if acc == nil {
			continue
		}
		if isNew {
			created++
		} else {
			updated++
		}
	}
	return created, updated, nil
}
