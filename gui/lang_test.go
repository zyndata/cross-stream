package main

import (
	"go/scanner"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/zyndata/cross-stream/internal/i18ncheck"
)

const i18nDir = "frontend/src/i18n"

// testTexts is a texts in lang with no prefs file.
func testTexts(lang string) *texts {
	return &texts{dicts: loadDicts(), choice: lang, lang: lang}
}

func TestDictParity(t *testing.T) {
	en, pl, err := i18ncheck.Load(i18nDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(en) < 100 {
		t.Fatalf("only %d keys", len(en))
	}
	for _, p := range i18ncheck.Parity(en, pl) {
		t.Error(p)
	}
	// The embedded copy Go uses is the same.
	d := loadDicts()
	if len(d[langEN]) != len(en) || len(d[langPL]) != len(pl) {
		t.Fatalf("embedded dictionaries: %d/%d keys, files %d/%d", len(d[langEN]), len(d[langPL]), len(en), len(pl))
	}
}

// properNames may appear in the frontend outside the dictionaries.
var properNames = []string{}

func TestNoPolishInFrontend(t *testing.T) {
	lit, err := i18ncheck.PolishLiterals("frontend/src", "i18n", properNames)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lit {
		t.Error("Polish text outside the dictionaries:", l)
	}
}

func TestFrontendKeysExist(t *testing.T) {
	en, _, err := i18ncheck.Load(i18nDir)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := i18ncheck.UsedKeys("frontend/src", "i18n")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) < 100 {
		t.Fatalf("only %d keys used", len(keys))
	}
	for _, k := range i18ncheck.Missing(en, keys) {
		t.Error("missing key:", k)
	}
}

var goKeyRE = regexp.MustCompile(`\.(?:T|N)\("([A-Za-z0-9_.]+)"`)

// TestGoTexts: every key the Go side uses exists in both languages, and no
// Go string literal outside the tests is Polish.
func TestGoTexts(t *testing.T) {
	en, pl, err := i18ncheck.Load(i18nDir)
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob("*.go")
	used := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range goKeyRE.FindAllStringSubmatch(string(b), -1) {
			used++
			if en[m[1]] == "" || pl[m[1]] == "" {
				t.Errorf("%s: key %s missing", f, m[1])
			}
		}
		var s scanner.Scanner
		fset := token.NewFileSet()
		s.Init(fset.AddFile(f, -1, len(b)), b, nil, 0)
		for {
			pos, tok, lit := s.Scan()
			if tok == token.EOF {
				break
			}
			if tok == token.STRING && strings.ContainsAny(lit, "ąćęłńóśźżĄĆĘŁŃÓŚŹŻ„") {
				t.Errorf("%s: Polish string literal %s", fset.Position(pos), lit)
			}
		}
	}
	if used < 30 {
		t.Fatalf("only %d Go keys found", used)
	}
}

func TestPluralIndex(t *testing.T) {
	tests := []struct {
		n      int
		pl, en int
	}{
		{0, 2, 1}, {1, 0, 0}, {2, 1, 1}, {4, 1, 1}, {5, 2, 1}, {11, 2, 1}, {12, 2, 1}, {14, 2, 1},
		{21, 2, 1}, {22, 1, 1}, {24, 1, 1}, {25, 2, 1}, {104, 1, 1}, {112, 2, 1}, {-3, 1, 1},
	}
	for _, tc := range tests {
		if got := pluralIndex(langPL, tc.n); got != tc.pl {
			t.Errorf("pl %d: %d, want %d", tc.n, got, tc.pl)
		}
		if got := pluralIndex(langEN, tc.n); got != tc.en {
			t.Errorf("en %d: %d, want %d", tc.n, got, tc.en)
		}
	}
	pl, en := testTexts(langPL), testTexts(langEN)
	for n, want := range map[int]string{1: "1 telefon", 3: "3 telefony", 5: "5 telefony"} {
		if got := pl.N("enroll.pathPhones", n, nil); got != want {
			t.Errorf("pl N(%d) = %q, want %q", n, got, want)
		}
	}
	if got := en.N("enroll.pathPhones", 2, nil); got != "2 phones" {
		t.Errorf("en N(2) = %q", got)
	}
}

// TestCoreJS runs the frontend's core.js (pluralIndex, resolveLang, fill,
// translateN) with node against the same table as Go; skipped without node.
func TestCoreJS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found")
	}
	core, err := filepath.Abs(filepath.Join(i18nDir, "core.js"))
	if err != nil {
		t.Fatal(err)
	}
	script := `
const c = await import(process.argv[1]);
const want = [[0,2,1],[1,0,0],[2,1,1],[4,1,1],[5,2,1],[12,2,1],[14,2,1],[21,2,1],[22,1,1],[25,2,1],[104,1,1],[112,2,1]];
const bad = [];
for (const [n, pl, en] of want) {
  if (c.pluralIndex('pl', n) !== pl) bad.push('pl ' + n);
  if (c.pluralIndex('en', n) !== en) bad.push('en ' + n);
}
const eq = (a, b, what) => { if (a !== b) bad.push(what + ': ' + a) };
eq(c.resolveLang('auto', 'pl-PL'), 'pl', 'auto pl-PL');
eq(c.resolveLang('auto', 'en-US'), 'en', 'auto en-US');
eq(c.resolveLang('auto', ''), 'en', 'auto empty');
eq(c.resolveLang('pl', 'en-US'), 'pl', 'pl');
eq(c.fill('{a} {b} {c}', { a: 1, b: 'x' }), '1 x {c}', 'fill');
const d = { pl: { k: '{n} telefon|{n} telefony|{n} telefonów' }, en: { k: '{n} phone|{n} phones', e: 'only en' } };
eq(c.translateN(d, 'pl', 'k', 22), '22 telefony', 'translateN pl');
eq(c.translateN(d, 'en', 'k', 1), '1 phone', 'translateN en');
eq(c.translate(d, 'pl', 'e'), 'only en', 'fallback');
eq(c.translate(d, 'pl', 'none'), 'none', 'missing');
eq(c.locale('pl'), 'pl-PL', 'locale');
if (bad.length) { console.log(bad.join('\n')); process.exit(1) }
`
	u := "file:///" + strings.TrimPrefix(filepath.ToSlash(core), "/")
	out, err := exec.Command(node, "--input-type=module", "-e", script, u).CombinedOutput()
	if err != nil {
		t.Fatalf("core.js: %v\n%s", err, out)
	}
}

func TestResolveLang(t *testing.T) {
	tests := []struct{ choice, system, want string }{
		{"auto", "pl_PL.UTF-8", "pl"},
		{"auto", "pl-PL", "pl"},
		{"auto", "pl", "pl"},
		{"auto", "en_GB.UTF-8", "en"},
		{"auto", "de-DE", "en"},
		{"auto", "C.UTF-8", "en"},
		{"auto", "", "en"},
		{"auto", "plt", "en"},
		{"pl", "en-US", "pl"},
		{"en", "pl-PL", "en"},
	}
	for _, tc := range tests {
		if got := resolveLang(tc.choice, tc.system); got != tc.want {
			t.Errorf("%s/%s: %s, want %s", tc.choice, tc.system, got, tc.want)
		}
	}
}

func TestTextsSet(t *testing.T) {
	x := &texts{path: filepath.Join(t.TempDir(), langFile), system: "pl_PL.UTF-8", dicts: loadDicts(), choice: langAuto, lang: langPL}
	if v, err := x.Set("en"); err != nil || v.Lang != langEN || x.T("common.save", nil) != "Save" {
		t.Fatalf("set en: %+v %v", v, err)
	}
	if _, err := x.Set("de"); err == nil {
		t.Fatal("de accepted")
	}
	if v, err := x.Set("auto"); err != nil || v != (LangView{Choice: langAuto, Lang: langPL}) || x.T("common.save", nil) != "Zapisz" {
		t.Fatalf("set auto: %+v %v", v, err)
	}
	b, err := os.ReadFile(x.path)
	if err != nil || !strings.Contains(string(b), `"auto"`) {
		t.Fatalf("prefs %q %v", b, err)
	}
	if got := x.T("no.such.key", nil); got != "no.such.key" {
		t.Fatalf("missing key: %q", got)
	}
}

// TestEventTextEnglish: notifications follow the language setting.
func TestEventTextEnglish(t *testing.T) {
	en := testTexts(langEN)
	tests := []struct {
		e    hubEvent
		want string
	}{
		{hubEvent{Type: "mode_changed", Mode: "DIRECT"}, "Hub mode: direct mode."},
		{hubEvent{Type: "speedtest_done", Value: 80, Up: 30}, "Link test finished: download 80 Mb/s, upload 30 Mb/s."},
		{hubEvent{Type: "relay_left", Relay: "u2", Name: "Modem", Reason: "leaving: limit"}, "Local link disconnected (data limit used up)."},
		{hubEvent{Type: "relay_left", Reason: "control lost"}, "The phone left (connection lost)."},
		{hubEvent{Type: "cap_80", Value: 500}, "80% of the 500 MB limit used."},
		{hubEvent{Type: "mode_changed", Mode: "WEIRD"}, "Hub mode: WEIRD."},
	}
	for _, tc := range tests {
		if _, body := eventText(en, tc.e); body != tc.want {
			t.Errorf("%+v: %q, want %q", tc.e, body, tc.want)
		}
	}
	if title, _ := eventText(en, hubEvent{Type: "relay_joined"}); title != "CrossStream: Phone" {
		t.Errorf("title %q", title)
	}
	// Every toggled type has a text in both languages.
	for _, l := range []string{langEN, langPL} {
		for _, typ := range notifyTypes {
			if title, body := eventText(testTexts(l), hubEvent{Type: typ, Name: "anna", Mode: "BONDED", Value: 5}); title == "" || body == "" || strings.Contains(body, "go.") {
				t.Errorf("%s %s: %q %q", l, typ, title, body)
			}
		}
	}
}
