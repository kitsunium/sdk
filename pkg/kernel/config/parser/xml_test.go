package parser_test

import (
	"os"
	"testing"

	"github.com/kistunium/sdk/pkg/kernel/config/parser"
	"github.com/stretchr/testify/assert"
)

func TestXMLLoad(t *testing.T) {
	tempFile, err := os.CreateTemp("", "test_config_*.xml")
	assert.NoError(t, err)
	defer os.Remove(tempFile.Name())

	_, err = tempFile.Write(XMLContent)
	assert.NoError(t, err)

	err = tempFile.Close()
	assert.NoError(t, err)

	xmlParser := parser.XML{Path: tempFile.Name()}

	config, err := xmlParser.Load()
	assert.NoError(t, err)
	assert.NotNil(t, config)
	assert.Equal(t, ExpectedConfig, config)
}

func TestNoXMLLoad(t *testing.T) {
	tempFile, err := os.CreateTemp("", "test_config_*.xxx")
	assert.NoError(t, err)
	defer os.Remove(tempFile.Name())

	_, err = tempFile.Write(XMLContent)
	assert.NoError(t, err)

	err = tempFile.Close()
	assert.NoError(t, err)

	xmlParser := parser.XML{Path: tempFile.Name()}

	config, err := xmlParser.Load()
	assert.Error(t, err)
	assert.Nil(t, config)
}

func TestXMLType(t *testing.T) {
	xmlParser := parser.XML{}
	assert.Equal(t, "xml", xmlParser.Type())
}
