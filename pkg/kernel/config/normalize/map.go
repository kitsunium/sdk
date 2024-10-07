package normalize

import (
	"fmt"
	"strings"
)

func Map(input map[string]any) map[string]string {
	output := make(map[string]string)

	reduce(output, input)

	return output
}

func reduce(output map[string]string, input map[string]any, prefix ...string) {
	for key, value := range input {
		switch v := value.(type) {
		case map[string]any:
			reduce(output, v, append(prefix, key)...)
		case []any:
			for i, item := range v {
				reduce(output, map[string]any{fmt.Sprintf("%v.%v", key, i): item}, prefix...)
			}
		default:
			output[Key(strings.Join(append(prefix, key), "."))] = Value(fmt.Sprintf("%v", value))
		}
	}
}
