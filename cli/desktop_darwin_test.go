//go:build darwin

package cli

import (
	"path/filepath"
	"testing"
)

func TestDistributionRoot(t *testing.T) {
	tests := []struct {
		name string
		exe  string
		want string
	}{
		{
			name: "raw executable",
			exe:  "/tmp/release/codebuddy-gateway",
			want: "/tmp/release",
		},
		{
			name: "application bundle",
			exe:  "/tmp/release/CodeBuddy2API.app/Contents/MacOS/CodeBuddy2API",
			want: "/tmp/release",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := distributionRoot(tt.exe); got != tt.want {
				t.Fatalf("distributionRoot(%q) = %q, want %q", tt.exe, got, tt.want)
			}
		})
	}
}

func TestProcessMatchesExecutable(t *testing.T) {
	root := "/tmp/release"
	tests := []struct {
		name        string
		processPath string
		exe         string
		want        bool
	}{
		{
			name:        "relative raw binary",
			processPath: "./codebuddy-gateway",
			exe:         filepath.Join(root, "codebuddy-gateway"),
			want:        true,
		},
		{
			name:        "absolute app bundle binary",
			processPath: filepath.Join(root, "CodeBuddy2API.app", "Contents", "MacOS", "CodeBuddy2API"),
			exe:         filepath.Join(root, "CodeBuddy2API.app", "Contents", "MacOS", "CodeBuddy2API"),
			want:        true,
		},
		{
			name:        "different installation",
			processPath: "/tmp/other/codebuddy-gateway",
			exe:         filepath.Join(root, "codebuddy-gateway"),
			want:        false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := processMatchesExecutable(root, tt.processPath, tt.exe); got != tt.want {
				t.Fatalf("processMatchesExecutable(%q, %q) = %t, want %t", tt.processPath, tt.exe, got, tt.want)
			}
		})
	}
}
