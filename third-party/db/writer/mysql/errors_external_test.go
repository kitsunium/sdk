package mysql_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	my "github.com/kitsunium/sdk/third-party/db/writer/mysql"
)

func Test_sentinelsCarryCodes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		code errs.Code
	}{
		{"client-init sentinel", my.ClientInitFailed, my.CodeMySQLClientInitFailed},
		{"insert sentinel", my.InsertFailed, my.CodeMySQLInsertFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: each exported sentinel must report its documented dotted-quad code.
			if !errs.HasCode(tc.err, tc.code) {
				t.Errorf("%s: HasCode(%v, %v) = false", tc.name, tc.err, tc.code)
			}
		})
	}
}
