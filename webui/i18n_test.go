package webui

import (
	"bytes"
	"os"
	"testing"

	"github.com/zyndata/cross-stream/internal/i18ncheck"
)

// The panel's dictionaries (src/i18n, phase 22): same keys and placeholders
// in both languages, every key the code uses exists, no Polish text outside
// the dictionaries, and the language core is the GUI's file.

func TestDictParity(t *testing.T) {
	en, pl, err := i18ncheck.Load("src/i18n")
	if err != nil {
		t.Fatal(err)
	}
	if len(en) < 100 {
		t.Fatalf("only %d keys", len(en))
	}
	for _, p := range i18ncheck.Parity(en, pl) {
		t.Error(p)
	}
}

func TestNoPolishInPanel(t *testing.T) {
	lit, err := i18ncheck.PolishLiterals("src", "i18n", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lit {
		t.Error("Polish text outside the dictionaries:", l)
	}
}

func TestPanelKeysExist(t *testing.T) {
	en, _, err := i18ncheck.Load("src/i18n")
	if err != nil {
		t.Fatal(err)
	}
	keys, err := i18ncheck.UsedKeys("src", "i18n")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) < 80 {
		t.Fatalf("only %d keys used", len(keys))
	}
	for _, k := range i18ncheck.Missing(en, keys) {
		t.Error("missing key:", k)
	}
}

// TestCoreSameAsGUI: the plural and fill rules are one file in two places
// (the GUI's copy is tested with node in gui/lang_test.go).
func TestCoreSameAsGUI(t *testing.T) {
	a, err := os.ReadFile("src/i18n/core.js")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../gui/frontend/src/i18n/core.js")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("webui/src/i18n/core.js differs from gui/frontend/src/i18n/core.js")
	}
}
