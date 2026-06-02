package mysql_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/writer"
	_ "github.com/kitsunium/sdk/third-party/db/writer/mysql"
)

func Test_mysqlRegistered(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		key  writer.Name
	}{
		{"mysql is registered", "mysql"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: importing the package self-registers the mysql factory.
			if !tc.key.Known() {
				t.Errorf("%s: writer key %q not registered", tc.name, tc.key)
			}
		})
	}
}
