package parser

import (
	"errors"
	"os"
	"strings"

	"github.com/kistunium/sdk/pkg/kernel/config/normalize"
)

// Args is a configuration parser for command line arguments.
type ARGS struct {
}

// Type returns the type of the parser.
func (e *ARGS) Type() string {
	return "args"
}

// Load parses the command line arguments and returns a configuration map.
// It expects arguments in the form of key=value or key value pairs.
// If an argument is in the form of key=value, it splits the argument into key and value.
// If an argument is in the form of key value, it pairs the key with the next argument as the value.
// Returns an error if an argument is not followed by a value.
//
// Returns:
//   - map[string]string: A map containing the parsed configuration.
//   - error: An error if the arguments are not in the expected format.
func (e *ARGS) Load() (map[string]string, error) {
	args := os.Args[1:]
	config := make(map[string]string)

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.Contains(arg, "=") {
			parts := strings.SplitN(arg, "=", 2)
			config[normalize.Key(parts[0])] = normalize.Value(parts[1])
		} else if i+1 < len(args) {
			config[normalize.Key(arg)] = normalize.Value(args[i+1])
			i++
		} else {
			return nil, errors.New("invalid argument format, expected a value after key")
		}
	}

	return config, nil
}
