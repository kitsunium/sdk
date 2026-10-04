// Package main — the platforms table's reader.
package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// tableScript writes a platforms script around the given here-document body.
func tableScript(body string) string {
	return "#!/usr/bin/env bash\nset -euo pipefail\n\ncat <<'CELLS'\n" + body + "CELLS\n"
}

// Test_parsePlatforms pins what the reader accepts — the cells of the one
// here-document, in order, a Windows checkout's carriage returns ignored —
// and that it refuses, by name, every other shape: no table, a table never
// closed, an empty one, a line that is no cell, a cell listed twice.
func Test_parsePlatforms(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// src is the script.
		src string
		// want are the cells, when it is accepted.
		want []string
		// wantErr is a fragment of the refusal, when it is refused.
		wantErr string
	}
	tests := []tc{
		{name: "two cells, in order", src: tableScript("linux/amd64\nwindows/amd64\n"), want: []string{"linux/amd64", "windows/amd64"}},
		{name: "a CRLF checkout reads the same", src: strings.ReplaceAll(tableScript("linux/amd64\nillumos/amd64\n"), "\n", "\r\n"), want: []string{"linux/amd64", "illumos/amd64"}},
		{name: "no here-document", src: "#!/usr/bin/env bash\necho linux/amd64\n", wantErr: "no line"},
		{name: "a table never closed", src: "cat <<'CELLS'\nlinux/amd64\n", wantErr: "never closed"},
		{name: "an empty table", src: tableScript(""), wantErr: "lists no cell"},
		{name: "a line that is no cell", src: tableScript("linux/amd64\nlinux amd64\n"), wantErr: `"linux amd64" is no goos/goarch cell`},
		{name: "a cell listed twice", src: tableScript("linux/amd64\nlinux/amd64\n"), wantErr: "listed twice"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		cells, err := parsePlatforms(c.src)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("parsePlatforms = %v, %v; want an error containing %q", cells, err, c.wantErr)
			}
			return
		}
		if err != nil {
			t.Fatalf("parsePlatforms = %v, want nil", err)
		}
		if got := cellNames(cells); !slices.Equal(got, c.want) {
			t.Fatalf("cells = %q, want %q", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_readPlatforms pins that a table that cannot be read, or reads wrong,
// fails with its path named.
func Test_readPlatforms(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bad := filepath.Join(dir, "platforms.sh")
	if err := os.WriteFile(bad, []byte("echo nothing\n"), 0o600); err != nil {
		t.Fatalf("staging: %v", err)
	}
	if _, err := readPlatforms(filepath.Join(dir, "absent.sh")); err == nil {
		t.Fatal("readPlatforms of a missing file = nil, want an error")
	}
	if _, err := readPlatforms(bad); err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("readPlatforms of a file with no table = %v, want an error naming %s", err, bad)
	}
}
