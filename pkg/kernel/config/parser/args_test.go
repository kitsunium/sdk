package parser_test

import (
	"os"
	"reflect"
	"testing"

	"github.com/kistunium/sdk/pkg/kernel/config/parser"
	"github.com/stretchr/testify/assert"
)

func TestArgsLoad(t *testing.T) {
	// Simulate command-line arguments.
	originalArgs := os.Args                   // Save the original arguments for restoration after the test.
	defer func() { os.Args = originalArgs }() // Restore the original arguments after the test.

	os.Args = []string{"cmd", "key1=value1", "key2=value2", "key3=value3", "key4", "value4", "K_E_Y5=value5", "key6", "'val=ue6'"}

	// Create an instance of Args and load the configuration.
	argsParser := &parser.ARGS{}
	config, err := argsParser.Load()

	// Check if there was an error.
	if err != nil {
		t.Fatalf("Load returned an error: %v", err)
	}

	// Expected configuration.
	expectedConfig := map[string]string{
		"key1":   "value1",
		"key2":   "value2",
		"key3":   "value3",
		"key4":   "value4",
		"k.e.y5": "value5",
		"key6":   "val=ue6",
	}

	// Verify that the configuration is correct.
	if !reflect.DeepEqual(config, expectedConfig) {
		t.Errorf("Expected config %v, but got %v", expectedConfig, config)
	}
}

func TestARGSType(t *testing.T) {
	argsParser := &parser.ARGS{}
	assert.Equal(t, "args", argsParser.Type())
}
