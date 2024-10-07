package parser_test

import (
	"os"
	"testing"

	"github.com/kistunium/sdk/pkg/kernel/config/parser"
	"github.com/stretchr/testify/assert"
)

func TestYAMLLoad(t *testing.T) {
	tempFile, err := os.CreateTemp("", "test_config_*.yaml")
	assert.NoError(t, err)
	defer os.Remove(tempFile.Name())

	_, err = tempFile.Write(YAMLContent)
	assert.NoError(t, err)

	err = tempFile.Close()
	assert.NoError(t, err)

	yamlParser := parser.YAML{Path: tempFile.Name()}

	config, err := yamlParser.Load()
	assert.NoError(t, err)
	assert.NotNil(t, config)
	assert.Equal(t, ExpectedConfig, config)
}

func TestNoYAMLLoad(t *testing.T) {
	tempFile, err := os.CreateTemp("", "test_config_*.xxx")
	assert.NoError(t, err)
	defer os.Remove(tempFile.Name())

	_, err = tempFile.Write(YAMLContent)
	assert.NoError(t, err)

	err = tempFile.Close()
	assert.NoError(t, err)

	yamlParser := parser.YAML{Path: tempFile.Name()}

	config, err := yamlParser.Load()
	assert.Error(t, err)
	assert.Nil(t, config)
}

// TestYAMLType tests the Type function for the YAML parser.
func TestYAMLType(t *testing.T) {
	yamlParser := parser.YAML{}
	assert.Equal(t, "yaml", yamlParser.Type())
}
