package config_test

import (
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/config"
)

type conf struct {
	Name string `json:"name"`
}

// TestFacade loads an env layer through the public facade.
func TestFacade(t *testing.T) {
	t.Setenv("SVC_NAME", "kitsune")
	var c conf
	//: a single env source loads into the typed struct.
	if err := config.Load(&c, config.EnvSource("SVC")); err != nil {
		t.Fatalf("Load: %v", err)
	}
	//: the env value decoded into the field.
	if c.Name != "kitsune" {
		t.Errorf("Name=%q, want kitsune", c.Name)
	}
}
