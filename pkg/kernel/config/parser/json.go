package parser

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"

	"github.com/kistunium/sdk/pkg/kernel/config/normalize"
)

type JSON struct {
	Path string
}

// Type Returns the file type "json"
//
// This function returns a string indicating the type of file to handle (in this case, JSON).
//
// Parameters:
// - None
//
// Returns:
// - string: file type "json"
func (j *JSON) Type() string {
	return "json"
}

// Load Loads and deserializes the JSON file
//
// This function opens the JSON file at the specified path, reads its content,
// deserializes it into a map[string]any, and then normalizes it into a map[string]string.
//
// Parameters:
// - None
//
// Returns:
// - map[string]string: normalized configuration map from the JSON content
// - error: error if any issues occurred during loading or deserialization
func (j *JSON) Load() (map[string]string, error) {
	if ext := path.Ext(j.Path); ext != ".json" {
		return nil, fmt.Errorf("invalid file extension: %s", ext)
	}

	var config map[string]any

	// Open the JSON file
	file, err := os.Open(j.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to open JSON file: %w", err)
	}
	defer file.Close()

	// Read the file content
	content, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("failed to read JSON file: %w", err)
	}

	// Unmarshal the JSON content into the config map
	err = json.Unmarshal(content, &config)
	if err != nil {
		return nil, fmt.Errorf("failed to parse JSON content: %w", err)
	}

	// Normalize the config map and return it
	return normalize.Map(config), nil
}
