package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// GUI language (phase 22): Go keeps the choice and resolves Automatic from
// the system locale; the frontend reads it (Language) and changes it
// (SetLanguage). Go texts come from the frontend's own dictionaries, so a
// key exists once for both sides.

//go:embed frontend/src/i18n/en.json frontend/src/i18n/pl.json
var dictFiles embed.FS

const langFile = "gui-lang.json"

// Language choices: auto follows the system locale.
const (
	langAuto = "auto"
	langPL   = "pl"
	langEN   = "en"
)

// LangView is what the frontend gets: the saved choice and the active language.
type LangView struct {
	Choice string `json:"choice"`
	Lang   string `json:"lang"`
}

// texts is the active language plus both dictionaries.
type texts struct {
	path   string // prefs file ("" = not saved)
	system string // system locale tag, e.g. "pl_PL.UTF-8" or "en-GB"
	dicts  map[string]map[string]string

	mu     sync.RWMutex
	choice string
	lang   string
}

func newTexts() *texts {
	x := &texts{system: systemLocale(), dicts: loadDicts(), choice: langAuto}
	if dir, err := os.UserConfigDir(); err == nil {
		x.path = filepath.Join(dir, "crossstream", langFile)
		var p struct {
			Choice string `json:"choice"`
		}
		if b, err := os.ReadFile(x.path); err == nil && json.Unmarshal(b, &p) == nil && validChoice(p.Choice) {
			x.choice = p.Choice
		}
	}
	x.lang = resolveLang(x.choice, x.system)
	return x
}

// loadDicts reads the embedded dictionaries; a broken file leaves that
// language empty (T then falls back to English, then to the key).
func loadDicts() map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, l := range []string{langEN, langPL} {
		d := map[string]string{}
		if b, err := dictFiles.ReadFile("frontend/src/i18n/" + l + ".json"); err == nil {
			_ = json.Unmarshal(b, &d)
		}
		out[l] = d
	}
	return out
}

func validChoice(c string) bool { return c == langAuto || c == langPL || c == langEN }

// resolveLang maps a choice and a system locale tag to pl or en (core.js resolveLang).
func resolveLang(choice, system string) string {
	if choice == langPL || choice == langEN {
		return choice
	}
	s := strings.ToLower(system)
	if s == "pl" || strings.HasPrefix(s, "pl_") || strings.HasPrefix(s, "pl-") || strings.HasPrefix(s, "pl.") {
		return langPL
	}
	return langEN
}

// pluralIndex is core.js pluralIndex: Polish 1 / 2–4 (not 12–14) / other, English 1 / other.
func pluralIndex(lang string, n int) int {
	if n < 0 {
		n = -n
	}
	if lang != langPL {
		if n == 1 {
			return 0
		}
		return 1
	}
	if n == 1 {
		return 0
	}
	d, h := n%10, n%100
	if d >= 2 && d <= 4 && (h < 12 || h > 14) {
		return 1
	}
	return 2
}

var placeholder = regexp.MustCompile(`\{(\w+)\}`)

// fill replaces {name} with params[name]; unknown placeholders stay.
func fill(s string, params map[string]any) string {
	if len(params) == 0 {
		return s
	}
	return placeholder.ReplaceAllStringFunc(s, func(m string) string {
		if v, ok := params[m[1:len(m)-1]]; ok {
			return fmt.Sprint(v)
		}
		return m
	})
}

// Lang returns the active language (pl or en).
func (x *texts) Lang() string {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.lang
}

func (x *texts) raw(key string) string {
	l := x.Lang()
	if s, ok := x.dicts[l][key]; ok {
		return s
	}
	if s, ok := x.dicts[langEN][key]; ok {
		return s
	}
	return key
}

// T is the text for key in the active language with params filled in.
func (x *texts) T(key string, params map[string]any) string {
	return fill(x.raw(key), params)
}

// N is T for a plural string ("one|few|many"); params["n"] defaults to n.
func (x *texts) N(key string, n int, params map[string]any) string {
	forms := strings.Split(x.raw(key), "|")
	i := min(pluralIndex(x.Lang(), n), len(forms)-1)
	p := map[string]any{"n": n}
	for k, v := range params {
		p[k] = v
	}
	return fill(forms[i], p)
}

// View is the saved choice and the active language.
func (x *texts) View() LangView {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return LangView{Choice: x.choice, Lang: x.lang}
}

// Set saves the choice and switches the language at once.
func (x *texts) Set(choice string) (LangView, error) {
	if !validChoice(choice) {
		return x.View(), fmt.Errorf("language %q (want auto, pl or en)", choice)
	}
	x.mu.Lock()
	x.choice, x.lang = choice, resolveLang(choice, x.system)
	path := x.path
	x.mu.Unlock()
	if path != "" {
		b, _ := json.Marshal(map[string]string{"choice": choice})
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return x.View(), fmt.Errorf("language: %w", err)
		}
		if err := os.WriteFile(path, b, 0o600); err != nil {
			return x.View(), fmt.Errorf("language: %w", err)
		}
	}
	return x.View(), nil
}

// Language returns the language choice and the active language (frontend start).
func (a *App) Language() LangView { return a.tr.View() }

// SetLanguage saves the language choice (auto, pl, en); Go texts switch at once.
func (a *App) SetLanguage(choice string) (LangView, error) { return a.tr.Set(choice) }
