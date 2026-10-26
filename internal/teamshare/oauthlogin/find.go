// Package oauthlogin drives a headless Chromium over the raw Chrome DevTools
// Protocol to complete an OAuth browser login unattended, using a WebAuthn
// virtual authenticator that holds a pre-issued service passkey.
//
// The caller supplies the authorization URL, the passkey material and a list
// of interaction steps (as data). The package reports the final redirect URL
// (the OAuth callback carrying code/state) and the updated signature counter.
package oauthlogin

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
)

// pathCandidates are the executable names looked up in PATH, in order.
var pathCandidates = []string{"chromium", "chromium-browser", "google-chrome", "chrome"}

// playwrightGlobs are globs relative to the Playwright browser cache.
var playwrightGlobs = []string{
	"chromium-*/chrome-linux*/chrome",
	"chromium_headless_shell-*/chrome-headless-shell-linux*/chrome-headless-shell",
}

// ErrChromiumNotFound is returned when no usable Chromium binary is located.
var ErrChromiumNotFound = errors.New("oauthlogin: no chromium binary found")

// FindChromium locates a Chromium-family browser binary. It first searches
// PATH, then the Playwright browser cache in ~/.cache/ms-playwright.
func FindChromium() (string, error) {
	for _, name := range pathCandidates {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrChromiumNotFound, err)
	}
	cache := filepath.Join(home, ".cache", "ms-playwright")
	for _, pattern := range playwrightGlobs {
		matches, err := filepath.Glob(filepath.Join(cache, pattern))
		if err != nil || len(matches) == 0 {
			continue
		}
		// Prefer the highest build directory name.
		sort.Sort(sort.Reverse(sort.StringSlice(matches)))
		for _, m := range matches {
			if isExecutableFile(m) {
				return m, nil
			}
		}
	}
	return "", ErrChromiumNotFound
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}
