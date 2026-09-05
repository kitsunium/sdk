package config_test

import (
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/config"
)

type conf struct {
	Name string `json:"name"`
	Port int    `json:"port"`
}

// The facade is a thin re-export, so what needs pinning is that an env layer
// reaches the typed struct through it: the prefix is stripped, the field name
// is matched through the json tag, and an absent variable leaves the zero
// value rather than erroring — a config loader that refused every unset
// optional would be unusable.
func TestFacade(t *testing.T) {
	// No t.Parallel: t.Setenv mutates a process-wide variable.
	type tc struct {
		name     string
		env      map[string]string
		wantName string
		wantPort int
	}
	tests := []tc{
		{"a single field", map[string]string{"SVC_NAME": "kitsune"}, "kitsune", 0},
		{
			"several fields decode by type",
			map[string]string{"SVC_NAME": "kitsune", "SVC_PORT": "8080"},
			"kitsune", 8080,
		},
		{"an absent variable leaves the zero value", nil, "", 0},
		{
			// The prefix is what scopes the layer; a variable outside it is
			// none of this config's business.
			"a variable outside the prefix is ignored",
			map[string]string{"OTHER_NAME": "nope"},
			"", 0,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		for k, v := range c.env {
			t.Setenv(k, v)
		}
		var got conf
		if err := config.Load(&got, config.EnvSource("SVC")); err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got.Name != c.wantName {
			t.Errorf("Name = %q, want %q", got.Name, c.wantName)
		}
		if got.Port != c.wantPort {
			t.Errorf("Port = %d, want %d", got.Port, c.wantPort)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}
