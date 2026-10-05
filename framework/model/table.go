// The table a database keeps a kit table in: one naming rule, shared by the
// runtime and the analyzer.

package model

import (
	"github.com/kitsunium/sdk/framework/model/internal/core"
)

// MaxTableLen is the longest table name a store on a database takes, in
// bytes: the SDK's document store over SQL keeps its index rows in the table
// named after it with "___ix", within PostgreSQL's 63.
const MaxTableLen int = core.MaxTableLen

// tableName is TableName's body: decl_gen.go writes TableName, from the
// design, as one call of it.
func tableName(service, node, suffix string) string {
	return core.TableName(service, node, suffix)
}
