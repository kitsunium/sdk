package journald_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/writer"
	_ "github.com/kitsunium/sdk/internal/service/writer/journald"
)

// closeIgnore drops a fixture Close error in the black-box tests: cleanup runs
// past assertion time, so a close failure must not mask the real result.
func closeIgnore(err error) {
	//: read the parameter so the discard is explicit, not a bare `_ =`.
	if err == nil {
		//: nothing to drop on the happy path.
		return
	}
}

func Test_journaldRegistered(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		key  writer.Name
	}{
		{"journald is registered", "journald"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: importing the package self-registers the journald factory.
			if !tc.key.Known() {
				t.Errorf("%s: writer key %q not registered", tc.name, tc.key)
			}
		})
	}
}
