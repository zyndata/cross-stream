//go:build windows

package main

import "golang.org/x/sys/windows"

// systemLocale is the user's first preferred UI language, e.g. "pl-PL".
func systemLocale() string {
	langs, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	if err != nil || len(langs) == 0 {
		return ""
	}
	return langs[0]
}
