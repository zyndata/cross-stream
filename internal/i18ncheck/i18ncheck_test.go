package i18ncheck

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestPlaceholders(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"no params", nil},
		{"{n} of {total}", []string{"n", "total"}},
		{"{b} {a} {b}", []string{"a", "b"}},
		{"{n} phone|{n} phones", []string{"n"}},
	}
	for _, tc := range tests {
		if got := Placeholders(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("%q: %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParity(t *testing.T) {
	tests := []struct {
		name   string
		en, pl Dict
		want   int
	}{
		{"ok", Dict{"a": "A {n}", "p": "{n} x|{n} xs"}, Dict{"a": "Ą {n}", "p": "{n} x|{n} xy|{n} xów"}, 0},
		{"missing key", Dict{"a": "A", "b": "B"}, Dict{"a": "Ą", "c": "C"}, 2},
		{"placeholder", Dict{"a": "{n}"}, Dict{"a": "{m}"}, 1},
		{"empty", Dict{"a": ""}, Dict{"a": "Ą"}, 1},
		{"plural forms", Dict{"p": "{n} x|{n} xs"}, Dict{"p": "{n} x|{n} xy"}, 1},
	}
	for _, tc := range tests {
		if got := Parity(tc.en, tc.pl); len(got) != tc.want {
			t.Errorf("%s: %v, want %d problems", tc.name, got, tc.want)
		}
	}
}

func TestScan(t *testing.T) {
	dir := t.TempDir()
	write := func(name, s string) {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("A.svelte", "<p>{t('a.b')}</p>\n<p>Zapisz</p>\n<b>{tn('c.d', 2)}</b> {t('dyn.' + v)}\n<i>Kraków</i>\n<p>{t('common.no')}</p>\n")
	write("lib/x.js", "const s = 'Łącze'\n")
	write("i18n/pl.json", `{"a.b": "Ąę"}`)
	lit, err := PolishLiterals(dir, "i18n", []string{"Kraków"})
	if err != nil || len(lit) != 2 {
		t.Fatalf("literals %v %v", lit, err)
	}
	keys, err := UsedKeys(dir, "i18n")
	if err != nil || !slices.Equal(keys, []string{"a.b", "c.d", "common.no"}) {
		t.Fatalf("keys %v %v", keys, err)
	}
	if m := Missing(Dict{"a.b": "x"}, keys); !slices.Equal(m, []string{"c.d", "common.no"}) {
		t.Fatalf("missing %v", m)
	}
	write("i18n/en.json", `{"a.b": "Ab"}`)
	en, pl, err := Load(filepath.Join(dir, "i18n"))
	if err != nil || en["a.b"] != "Ab" || pl["a.b"] != "Ąę" {
		t.Fatalf("load %v %v %v", en, pl, err)
	}
}
