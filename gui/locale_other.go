//go:build !windows

package main

import "os"

// systemLocale is the user's message locale (LC_ALL, LC_MESSAGES, LANG).
func systemLocale() string {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}
