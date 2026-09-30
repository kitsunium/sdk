package kit

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// legacyPage is kept by a store that kept its versions before kit sealed
// them.
type legacyPage struct {
	ID    string `json:"id"`
	Owner string `json:"owner" kit:"subject"`
	Body  string `json:"body" kit:"personal"`
}

var legacyPagesSvc = NewService("legacy-pages", "Pages whose versions were written before kit sealed them, for the revisions tests.")

var legacyPages = legacyPagesSvc.Store("pages", func(p legacyPage) string { return p.ID }, Revisions(2))

// `privacy seal` seals what a record's former versions keep in clear —
// written before their field was sealed — as it seals the record, and they
// read back as they were (ADR 0007 §4).
func TestPrivacySealSealsTheVersions(t *testing.T) {
	pinDataKeyWithoutFileStore(t)
	dir := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(dir, "legacy-pages"), 0o700))
	for file, content := range map[string]string{
		"pages.json": `{"p1":{"id":"p1","owner":"ann@x.dev","body":"new words"}}`,
		"pages.json.versions": `{"p1":{"current":{"number":2,"at":"2026-09-27T10:00:00Z"},` +
			`"former":[{"number":1,"at":"2026-09-27T09:00:00Z","doc":{"id":"p1","owner":"ann@x.dev","body":"old words"}}]}}`,
	} {
		must(t, os.WriteFile(filepath.Join(dir, "legacy-pages", file), []byte(content), 0o600))
	}
	ctx := t.Context()
	var out, errOut bytes.Buffer
	cmd := NewApp("legacy-pages", legacyPagesSvc).With(DataDir(dir), Env(EnvProduction), Listen("127.0.0.1:1"), Logs(io.Discard))
	if code := cmd.privacyCommand(ctx, []string{"seal", "legacy-pages/store/pages"}, &out, &errOut); code != 0 ||
		out.String() != "legacy-pages/store/pages: 1 of 1 records sealed now, the others were already\n" {
		t.Fatalf("privacy seal: %d %q %q", code, out.String(), errOut.String())
	}
	if held := restingFiles(t, dir, "words", "ann@x.dev"); len(held) > 0 {
		t.Errorf("sealed, yet in clear in %v", held)
	}
	app := NewApp("legacy-pages", legacyPagesSvc).With(DataDir(dir), Listen("127.0.0.1:0"), Env(EnvProduction), Logs(io.Discard))
	must(t, app.Start(ctx))
	t.Cleanup(func() {
		if err := app.Stop(context.Background()); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	revs, err := legacyPages.Revisions(ctx, "p1")
	must(t, err)
	if len(revs) != 2 || revs[0].Number != 2 || revs[0].Value.Body != "new words" || revs[1].Value.Body != "old words" || revs[1].Value.Owner != "ann@x.dev" {
		t.Errorf("the versions sealed by the command: %+v", revs)
	}
}
