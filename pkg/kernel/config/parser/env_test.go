package parser_test

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/kistunium/sdk/pkg/kernel/config/parser"
	"github.com/stretchr/testify/assert"
)

func TestEnvLoad(t *testing.T) {
	// Clear all existing environment variables
	originalEnv := os.Environ() // Save original environment variables for later restoration
	defer func() {
		// Clear the environment and restore the original variables
		os.Clearenv()
		for _, e := range originalEnv {
			parts := strings.SplitN(e, "=", 2)
			if len(parts) == 2 {
				os.Setenv(parts[0], parts[1])
			}
		}
	}()

	// Clear the environment and set simulated environment variables
	os.Clearenv()
	os.Setenv("APP_DB_HOST", "localhost")
	os.Setenv("APP_DB_PORT", "5432")
	os.Setenv("APP_ENV", "production")
	os.Setenv("MY_VAR_WITH_UNDERSCORES", "test_value")
	os.Setenv("KEY6", "val=ue6")

	// Create an instance of Env and load the configuration.
	envParser := &parser.ENV{}
	config, err := envParser.Load()

	// Check if there was an error.
	if err != nil {
		t.Fatalf("Load returned an error: %v", err)
	}

	// Expected configuration.
	expectedConfig := map[string]string{
		"app.db.host":             "localhost",
		"app.db.port":             "5432",
		"app.env":                 "production",
		"my.var.with.underscores": "test_value",
		"key6":                    "val=ue6",
	}

	// Verify that the configuration is correct.
	if !reflect.DeepEqual(config, expectedConfig) {
		t.Errorf("Expected config %v, but got %v", expectedConfig, config)
	}
}

func TestENVType(t *testing.T) {
	// Test if the Type method correctly returns "env"
	envParser := &parser.ENV{}
	assert.Equal(t, "env", envParser.Type())
}
