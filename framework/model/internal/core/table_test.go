package core_test

import (
	"strings"
	"testing"

	model "github.com/kitsunium/sdk/framework/model/internal/core"
)

// A table is named after its service and its node, '-' and '.' written
// '_'; a name the rule cannot keep ends with a digest of the whole, within
// the length a store's table takes.
func TestTablesAreNamedAfterTheirNodes(t *testing.T) {
	for _, c := range []struct{ service, node, suffix, want string }{
		{"books", "entries", "", "books__entries"},
		{"moderation.intake", "cases", "", "moderation_intake__cases"},
		{"shop", "line-items", "__history", "shop__line_items__history"},
	} {
		if got := model.TableName(c.service, c.node, c.suffix); got != c.want {
			t.Errorf("%s/%s%s: %q", c.service, c.node, c.suffix, got)
		}
	}
	for _, c := range []struct{ service, node string }{
		{"shop", "Items"},
		{"shop", "a_-b"},
		{"sqlite", "x"},
		{"s", strings.Repeat("n", 70)},
	} {
		got := model.TableName(c.service, c.node, "")
		if len(got) > model.MaxTableLen || strings.Contains(got, "___") || strings.HasPrefix(got, "sqlite_") || got != strings.ToLower(got) {
			t.Errorf("%s/%s: %q", c.service, c.node, got)
		}
	}
	if model.TableName("shop", "Items", "") == model.TableName("shop", "items", "") {
		t.Error("two names that differ by case share a table")
	}
}
