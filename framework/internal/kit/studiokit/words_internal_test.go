package studiokit

import (
	"errors"
	"slices"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/pkg/v1/app/i18n"
)

// The catalogues after English open against the English kit's core hands
// them, the language each falls back to: French, its keys sorted, its
// sentences rendered by the SDK's i18n with their placeholders filled, and
// no other language. Whether every catalogue holds exactly English's keys is
// the core's check (TestKitSpeaksEveryLanguage), which reads kit's table.
func TestTheCataloguesAfterEnglishOpen(t *testing.T) {
	french, err := catalogueFile("fr")
	if err != nil {
		t.Fatal(err)
	}
	english := make(map[string]string, len(french))
	for key := range french {
		english[key] = "English: " + key
	}
	tr, err := openWords(english)
	if err != nil {
		t.Fatal(err)
	}
	if got := tr.Languages(); !slices.Equal(got, []string{"fr"}) {
		t.Errorf("the languages after English are %v", got)
	}
	keys := tr.Keys("fr")
	if len(keys) != len(french) || !slices.IsSorted(keys) {
		t.Errorf("French has %d keys, sorted %v; its file %d", len(keys), slices.IsSorted(keys), len(french))
	}
	if got := tr.Keys("de"); got != nil {
		t.Errorf("a language kit does not speak has keys: %v", got)
	}
	got, err := tr.Render("fr", "list.and", map[string]string{"first": "a", "rest": "b"})
	if err != nil || got != "a et b" {
		t.Errorf("list.and in French: %q, %v", got, err)
	}
	if _, err := tr.Render("fr", "list.and", map[string]string{"first": "a"}); !errors.Is(err, i18n.ArgumentMissing) {
		t.Errorf("a sentence without its placeholder: %v", err)
	}
	if _, err := tr.Render("de", "list.and", nil); !errors.Is(err, i18n.UnsupportedLanguage) {
		t.Errorf("a language kit does not speak: %v", err)
	}
}

// Enable plugs the catalogues in: plug.Words opens them.
func TestEnablePlugsTheCataloguesIn(t *testing.T) {
	saved := plug.Words.Load()
	t.Cleanup(func() { plug.Words.Store(saved) })
	plug.Words.Store(nil)
	Enable()
	cfg := plug.Words.Load()
	if cfg == nil || cfg.Open == nil {
		t.Fatal("Enable plugged no catalogue in")
	}
}
