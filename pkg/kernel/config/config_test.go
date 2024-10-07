package config_test

import (
	"os"
	"strings"
	"testing"

	"github.com/kistunium/sdk/pkg/kernel/config"
	"github.com/kistunium/sdk/pkg/kernel/config/parser"
	"github.com/stretchr/testify/assert"
)

func TestNewConfig(t *testing.T) {

	os.Setenv("ENV_VALUE", "env_value")
	os.Setenv("ENV_VALUE_TO_OVERRIDE_BY_ARGS", "env_value")
	os.Setenv("ENV_VALUE_TO_OVERRIDE_BY_XML", "env_value")
	os.Setenv("ENV_VALUE_TO_OVERRIDE_BY_JSON", "env_value")
	os.Setenv("ENV_VALUE_TO_OVERRIDE_BY_YAML", "env_value")

	os.Args = []string{
		"cmd",
		"ENV_VALUE_TO_OVERRIDE_BY_ARGS=args_value",
		"ARGS_VALUE_TO_OVERRIDE_BY_XML=args_value",
		"ARGS_VALUE_TO_OVERRIDE_BY_JSON=args_value",
		"ARGS_VALUE_TO_OVERRIDE_BY_YAML=args_value",
		"ARGS_VALUE_TO_KEEP=args_value",
	}

	jsonContent := `
{	
	"args": {
		"value": {
			"to": {
				"override": {
					"by": {
						"json": "json_value"
					}
				}
			}
		}
	},
	"xml": {
		"value": {
			"to": {
				"override": {
					"by": {
						"json": "json_value"
					}
				}
			}
		}
	},
	"json": {
        "value": {
            "to": {
				"override": {
					"by": {
						"yaml": "json_value"
					}
                },
                "keep": "json_value"
            }
        }
    },
    "env": {
        "value": {
            "to": {
                "override": {
					"by": {
						"json": "json_value"
					}
                }
            }
        }
    }
}
`

	xmlContent := `
<?xml version="1.0" encoding="UTF-8"?>
<config>
	<env>
		<value>
			<to>
				<override>
					<by>
						<xml>
							xml_value
						</xml>
						<yaml>
							xml_value
						</yaml>
					</by>
				</override>
				<keep>
					xml_value
				</keep>
			</to>
		</value>
	</env>
	<args>
		<value>
			<to>
				<override>
					<by>
						<xml>
							xml_value
						</xml>
					</by>
				</override>
			</to>
		</value>
	</args>
</config>
`

	yamlContent := `
env:
  value:
    to:
      override: 
        by:
          yaml: yaml_value

args:
  value:
    to:
      override: 
        by:
          yaml: yaml_value
xml:
  value:
    to:
      override: 
        by:
          yaml: yaml_value

json:
  value:
    to:
      override: 
        by:
          yaml: yaml_value
`

	jsonFile, err := os.CreateTemp("", "test_config_*.json")
	assert.NoError(t, err)
	defer os.Remove(jsonFile.Name())
	_, err = jsonFile.Write([]byte(strings.TrimSpace(jsonContent)))
	assert.NoError(t, err)
	err = jsonFile.Close()
	assert.NoError(t, err)

	yamlFile, err := os.CreateTemp("", "test_config_*.yaml")
	assert.NoError(t, err)
	defer os.Remove(yamlFile.Name())
	_, err = yamlFile.Write([]byte(strings.TrimSpace(yamlContent)))
	assert.NoError(t, err)
	err = yamlFile.Close()
	assert.NoError(t, err)

	xmlFile, err := os.CreateTemp("", "test_config_*.xml")
	assert.NoError(t, err)
	defer os.Remove(xmlFile.Name())
	_, err = xmlFile.Write([]byte(strings.TrimSpace(xmlContent)))
	assert.NoError(t, err)
	err = xmlFile.Close()
	assert.NoError(t, err)

	c := config.New(
		&parser.ENV{},
		&parser.ARGS{},
		&parser.XML{Path: xmlFile.Name()},
		&parser.JSON{Path: jsonFile.Name()},
		&parser.YAML{Path: yamlFile.Name()},
	)

	assert.Nil(t, c.Load())
	assert.Equal(t, "env_value", c.Get("env.value", nil))

	// ENV values should be overridden
	assert.Equal(t, "args_value", c.Get("env.value.to.override.by.args", nil))
	assert.Equal(t, "xml_value", c.Get("env.value.to.override.by.xml", nil))
	assert.Equal(t, "json_value", c.Get("env.value.to.override.by.json", nil))
	assert.Equal(t, "yaml_value", c.Get("env.value.to.override.by.yaml", nil))

	// ARGS values should be overridden
	assert.Equal(t, "xml_value", c.Get("args.value.to.override.by.xml", nil))
	assert.Equal(t, "json_value", c.Get("args.value.to.override.by.json", nil))
	assert.Equal(t, "yaml_value", c.Get("args.value.to.override.by.yaml", nil))

	// XML values should be overridden
	assert.Equal(t, "json_value", c.Get("xml.value.to.override.by.json", nil))
	assert.Equal(t, "yaml_value", c.Get("xml.value.to.override.by.yaml", nil))

	// JSON values should be overridden
	assert.Equal(t, "yaml_value", c.Get("json.value.to.override.by.yaml", nil))
}
