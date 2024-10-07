package parser_test

import (
	"os"
	"testing"

	"github.com/kistunium/sdk/pkg/kernel/config/parser"
	"github.com/stretchr/testify/assert"
)

func TestJSONLoad(t *testing.T) {
	tempFile, err := os.CreateTemp("", "test_config_*.json")
	assert.NoError(t, err)
	defer os.Remove(tempFile.Name())

	_, err = tempFile.Write(JSONContent)
	assert.NoError(t, err)

	err = tempFile.Close()
	assert.NoError(t, err)

	jsonParser := &parser.JSON{Path: tempFile.Name()}

	config, err := jsonParser.Load()
	assert.NoError(t, err)
	assert.NotNil(t, config)
	assert.Equal(t, ExpectedConfig, config)
}

func TestNoJSONLoad(t *testing.T) {
	tempFile, err := os.CreateTemp("", "test_config_*.xxx")
	assert.NoError(t, err)
	defer os.Remove(tempFile.Name())

	_, err = tempFile.Write(JSONContent)
	assert.NoError(t, err)

	err = tempFile.Close()
	assert.NoError(t, err)

	jsonParser := parser.JSON{Path: tempFile.Name()}

	config, err := jsonParser.Load()
	assert.Error(t, err)
	assert.Nil(t, config)
}

func TestJSONType(t *testing.T) {
	jsonParser := &parser.JSON{}
	assert.Equal(t, "json", jsonParser.Type())
}
