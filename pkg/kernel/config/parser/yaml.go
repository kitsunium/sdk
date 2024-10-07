package parser

import (
	"fmt"
	"io"
	"os"
	"path"

	"github.com/kistunium/sdk/pkg/kernel/config/normalize"
	"gopkg.in/yaml.v3"
)

type YAML struct {
	Path string
}

// Type Returns the file type "yaml"
//
// This function returns a string indicating the type of file to handle (in this case, YAML).
//
// Parameters:
// - None
//
// Returns:
// - string: file type "yaml"
func (y *YAML) Type() string {
	return "yaml"
}

// Load Loads and deserializes the YAML file
//
// This function opens the YAML file at the specified path, reads its content,
// deserializes it, and then normalizes it into a map[string]string.
//
// Parameters:
// - None
//
// Returns:
// - map[string]string: normalized configuration map from the YAML content
// - error: error if any issues occurred during loading or deserialization
func (y *YAML) Load() (map[string]string, error) {
	if ext := path.Ext(y.Path); ext != ".yaml" && ext != ".yml" {
		return nil, fmt.Errorf("invalid file extension: %s", ext)
	}

	var config map[string]any

	// Open the YAML file
	file, err := os.Open(y.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to open YAML file: %w", err)
	}
	defer file.Close()

	// Read the file content
	content, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("failed to read YAML file: %w", err)
	}

	// Unmarshal the YAML content into the config map
	err = yaml.Unmarshal(content, &config)
	if err != nil {
		return nil, fmt.Errorf("failed to parse YAML content: %w", err)
	}

	// Normalize the config map and return it
	return normalize.Map(config), nil
}
