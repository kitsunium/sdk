package config

import (
	"sync"
)

type Parser interface {
	Load() (map[string]string, error)
	Type() string
}

type Config struct {
	parsers []Parser
	data    map[string]any
	mu      sync.RWMutex
}

// New creates a new Config instance
func New(parsers ...Parser) *Config {
	return &Config{
		parsers: parsers,
		data:    make(map[string]any),
	}
}

// Load loads configuration from a list of sources with a priority order
//
// Parameters:
// - sources: ...Source - A list of sources to load configuration from
//
// Returns:
// - err: error - Error if any issue occurs during loading
func (c *Config) Load() error {
	for _, source := range c.parsers {
		data, err := source.Load()
		if err != nil {
			return err
		}

		for key, value := range data {
			c.Set(key, value)
		}
	}

	return nil
}

// Set sets a value in the configuration
//
// Parameters:
// - key: string - The configuration key to set
// - value: any - The configuration value to set
func (c *Config) Set(key string, value any) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.data[key] = value
}

// Get retrieves a value from the configuration
//
// Parameters:
// - key: string - The configuration key to retrieve
//
// Returns:
// - value: any - The configuration value
func (c *Config) Get(key string, defaultValue any) any {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if value, ok := c.data[key]; ok {
		return value
	}

	return defaultValue
}
