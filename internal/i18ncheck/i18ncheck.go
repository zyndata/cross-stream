// Package i18ncheck holds the checks behind the dictionary tests of the hub
// GUI and the server panel (phase 22): both languages have the same keys and
// placeholders, plural strings have the right number of forms, every key
// the code uses exists, and no Polish text is left outside the dictionaries.
package i18ncheck

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Dict is one language: key → text.
type Dict map[string]string

// Load reads <dir>/en.json and <dir>/pl.json.
func Load(dir string) (en, pl Dict, err error) {
	read := func(name string) (Dict, error) {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("i18n: %w", err)
		}
		d := Dict{}
		if err := json.Unmarshal(b, &d); err != nil {
			return nil, fmt.Errorf("i18n %s: %w", name, err)
		}
		return d, nil
	}
	if en, err = read("en.json"); err != nil {
		return nil, nil, err
	}
	if pl, err = read("pl.json"); err != nil {
		return nil, nil, err
	}
	return en, pl, nil
}

var placeholderRE = regexp.MustCompile(`\{(\w+)\}`)

// Placeholders returns the sorted, distinct {name} placeholders of s.
func Placeholders(s string) []string {
	var out []string
	for _, m := range placeholderRE.FindAllStringSubmatch(s, -1) {
		if !slices.Contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	slices.Sort(out)
	return out
}

// Parity lists the problems between the two languages: a key in one only,
// different placeholders, an empty text, or a plural string ("|") that does
// not have 2 English and 3 Polish forms.
func Parity(en, pl Dict) []string {
	var out []string
	for k := range en {
		if _, ok := pl[k]; !ok {
			out = append(out, "only in en: "+k)
		}
	}
	for k := range pl {
		if _, ok := en[k]; !ok {
			out = append(out, "only in pl: "+k)
		}
	}
	for k, e := range en {
		p, ok := pl[k]
		if !ok {
			continue
		}
		if strings.TrimSpace(e) == "" || strings.TrimSpace(p) == "" {
			out = append(out, "empty text: "+k)
		}
		if !slices.Equal(Placeholders(e), Placeholders(p)) {
			out = append(out, fmt.Sprintf("placeholders differ: %s en %v pl %v", k, Placeholders(e), Placeholders(p)))
		}
		ne, np := strings.Count(e, "|")+1, strings.Count(p, "|")+1
		if (ne > 1 || np > 1) && (ne != 2 || np != 3) {
			out = append(out, fmt.Sprintf("plural forms: %s en %d pl %d (want 2 and 3)", k, ne, np))
		}
	}
	slices.Sort(out)
	return out
}

// polishLetters are the letters English never uses; „ is the Polish opening quote.
const polishLetters = "ąćęłńóśźżĄĆĘŁŃÓŚŹŻ„"

// polishWords are common Polish words without Polish letters (whole words,
// case-insensitive) that would give away an untranslated string.
var polishWords = regexp.MustCompile(`(?i)\b(nie|jest|tak|brak|albo|oraz|przez|telefon|telefony|zapisz|zapisano|ustawienia|serwer|hasło|dalej|wstecz|anuluj|zamknij|odśwież|dodaj|uruchom|zatrzymaj|sprawdź|sekund|minut|dni)\b`)

// textFiles lists the *.svelte and *.js files under root, skipping any
// directory named skip.
func textFiles(root, skip string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == skip {
			return filepath.SkipDir
		}
		if !d.IsDir() && (strings.HasSuffix(p, ".svelte") || strings.HasSuffix(p, ".js")) {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("i18n scan: %w", err)
	}
	return out, nil
}

// PolishLiterals scans the *.svelte and *.js files under root (the skip
// directory excluded) and returns "file:line: text" for every line with a
// Polish letter or a common Polish word. Lines containing an allow entry
// (proper names) are skipped.
func PolishLiterals(root, skip string, allow []string) ([]string, error) {
	files, err := textFiles(root, skip)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("i18n scan: %w", err)
		}
	lines:
		for i, line := range strings.Split(string(b), "\n") {
			for _, a := range allow {
				if strings.Contains(line, a) {
					continue lines
				}
			}
			if strings.ContainsAny(line, polishLetters) || polishWords.MatchString(stripCode(line)) {
				rel, _ := filepath.Rel(root, f)
				out = append(out, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(rel), i+1, strings.TrimSpace(line)))
			}
		}
	}
	return out, nil
}

// keyCallRE matches t('key' and tn('key' (the key may be followed by
// arguments) — dynamic keys ('prefix.' + v) end in a dot and are skipped.
var keyCallRE = regexp.MustCompile(`\btn?\('([A-Za-z0-9_.]+)'`)

// stripCode drops the dictionary calls from a line before the word check,
// so a key such as 'common.no' is not taken for the Polish word "no".
func stripCode(line string) string {
	return keyCallRE.ReplaceAllString(line, "")
}

// UsedKeys returns the literal keys of t()/tn() calls in the *.svelte and
// *.js files under root (skip excluded), sorted and distinct.
func UsedKeys(root, skip string) ([]string, error) {
	files, err := textFiles(root, skip)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("i18n scan: %w", err)
		}
		for _, m := range keyCallRE.FindAllStringSubmatch(string(b), -1) {
			if !strings.HasSuffix(m[1], ".") && !slices.Contains(out, m[1]) {
				out = append(out, m[1])
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

// Missing returns the keys not in d.
func Missing(d Dict, keys []string) []string {
	var out []string
	for _, k := range keys {
		if _, ok := d[k]; !ok {
			out = append(out, k)
		}
	}
	return out
}
