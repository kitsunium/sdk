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

// TableName is the table a database keeps a kit table in (ADR 0004): the
// service's name — qualified, for a module's —, two underscores and the
// node's name, then suffix, '-' and '.' written '_', lower case —
// "moderation.intake"'s store "cases" is moderation_intake__cases. A name
// the rule cannot keep as it is — upper case, longer than MaxTableLen,
// three underscores in a row, SQLite's own prefix — is cut and ends with a
// digest of the whole, so it stays one table's. The runtime and the
// analyzer name tables with it.
func TableName(service, node, suffix string) string {
	return core.TableName(service, node, suffix)
}
