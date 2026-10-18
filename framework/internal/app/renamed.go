package app

import (
	"errors"
	"reflect"
	"slices"
	"strings"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// The runtime names of ADR 0180 §3. The framework's own variables are read
// under the product's prefix — TODO_DATA_DIR for the product "todo" —, its
// probes and its reserved routes live under /_app/, and the struct tag it
// reads is app:"…". An old name met at the start is refused, naming the one
// that replaces it, for two minors (Q12), then forgotten: ignored,
// KIT_DATA_DIR would start the product on an empty default directory, and a
// field still tagged kit:"personal" would be stored in clear. Every old name
// the start meets is said at once, and the refusal carries CodeRenamed.

const (
	// frameworkName is the framework's name as the running product says it:
	// its probes' checks, the loops it writes, the settings it owns, the
	// actor of what it does on its own.
	frameworkName = "app"
	// formerName is the name it said before ADR 0180 step 4, and the name
	// its data rested under before step 5. A module may not take it while
	// that data may still be met under it: its tables, its privacy
	// service's directory, and the ids what its privacy service sealed is
	// bound to — read, never written, since step 5c (ADR 0180 Q6).
	formerName = "kit"
	// tagKey is the struct tag key the framework reads: app:"personal".
	tagKey = frameworkName
	// formerTagKey is the key it read before ADR 0180 step 4.
	formerTagKey = formerName
	// formerPrefix is the prefix its own variables had before ADR 0180 step 4.
	formerPrefix = "KIT"
	// renamedPublic is the refusal's public text.
	renamedPublic = "a name the framework no longer reads was met at the start: use the name that replaces it"

	// reservedPrefix is the framework's own route space: its probes, and
	// the Studio's reads in dev. A product's route never lies under it.
	reservedPrefix = "/_app/"
	// healthPrefix is where the three probes answer: live, ready, startup.
	healthPrefix = reservedPrefix + "health/"
	// formerReserved is the framework's route space before ADR 0180 step 4.
	// Its probes still answer there, logged as deprecated, and it stays
	// reserved, for two minors.
	formerReserved = "/_kit/"
)

// The names at rest of ADR 0180 step 5 (Q5): what the framework keeps rests
// under its own name — app_migrations, app_tables, app_queue__…, the privacy
// service app.privacy and its stores, .app/data in dev —, and what a former
// release kept under kit's is moved there, once, when the product meets it
// (renamed_rest.go, and the store subsystem on a database). A move never
// replaces: a present name already taken refuses the start, naming both.
const (
	// devDataDir is where the app keeps its data in dev when nothing names a
	// directory.
	devDataDir = "." + frameworkName + "/data"
	// formerDevDataDir is where it kept it before ADR 0180 step 5.
	formerDevDataDir = "." + formerName + "/data"
	// formerPrivacyService is the framework's own service's name before ADR
	// 0180 step 5: its directory in the data directory, its tables'
	// prefix, and what its stores' sealed values were bound to — read, never
	// written, since step 5c (Q6).
	formerPrivacyService = formerName + ".privacy"
	// formerSetTable is the version table of the framework's own set before
	// ADR 0180 step 5.
	formerSetTable = formerName + "_migrations"
	// formerTablesTable is its registry of its tables before then.
	formerTablesTable = formerName + "_tables"
	// formerQueueService is what a consumer's queue's table was named under
	// before then.
	formerQueueService = formerName + "_queue"
)

// The framework's own variables an operator sets, by their name after the
// product's prefix: each is read as <PRODUCT>_<stem> (ADR 0180 §3, Q3).
const (
	varEnv            string = model.VarEnv
	varAddr           string = model.VarAddr
	varDataDir        string = model.VarDataDir
	varTrustProxy     string = model.VarTrustProxy
	varAllowedHosts   string = model.VarAllowedHosts
	varLogLevel       string = model.VarLogLevel
	varSMTPURL        string = model.VarSMTPURL
	varRetention      string = model.VarRetention
	varSecrets               = "SECRETS"
	varSecretsKey            = "SECRETS_KEY"
	varTelemetry             = "TELEMETRY"
	varTelemetryGIDs         = "TELEMETRY_GIDS"
	varFileSuffix            = "_FILE"
	varDataKeyStem           = "DATA_KEY"
	varIndexKeyStem          = "INDEX_KEY"
	varSMTPURLFile           = varSMTPURL + varFileSuffix
	varSecretsKeyFile        = varSecretsKey + varFileSuffix
)

// operatorStems are those variables, the secrets' _FILE twins included, in
// the order a refusal names them. They are reserved in the product's own
// namespace: no setting, secret or database of the product may be read
// under one of them.
var operatorStems = []string{
	varAddr, varAllowedHosts, varDataDir, varDataKeyStem, varDataKeyStem + varFileSuffix, varEnv,
	varIndexKeyStem, varIndexKeyStem + varFileSuffix, varLogLevel, varRetention, varSecrets,
	varSecretsKey, varSecretsKeyFile, varSMTPURL, varSMTPURLFile, varTelemetry, varTelemetryGIDs,
	varTrustProxy,
}

// reservedPath reports whether a path lies in the framework's route space,
// its former one included.
func reservedPath(p string) bool {
	for _, prefix := range []string{reservedPrefix, formerReserved} {
		if p == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// renamedName is one old runtime name the start met, and the name that
// replaces it.
type renamedName struct {
	old, now string
}

// String is the name as the refusal lists it: "KIT_DATA_DIR -> TODO_DATA_DIR".
func (r renamedName) String() string { return r.old + " -> " + r.now }

// productVariable is the framework's variable stem under the prefix of the
// product named name: TODO_DATA_DIR.
func productVariable(name, stem string) string { return appPrefix(name) + "_" + stem }

// variable is the framework's variable stem under the app's prefix.
func (a *App) variable(stem string) string { return productVariable(a.name, stem) }

// reservedVariable reports whether a variable under the product's prefix is
// one of the framework's own.
func reservedVariable(prefix, name string) bool {
	stem, ok := strings.CutPrefix(name, prefix+"_")
	return ok && slices.Contains(operatorStems, stem)
}

// renamedVariables are the old names of the framework's variables set in
// the environment of the product named name, each with the name that
// replaces it; none for a product whose prefix is the old one, whose names
// they still are.
func renamedVariables(name string, getenv func(string) string) []renamedName {
	if appPrefix(name) == formerPrefix {
		return nil
	}
	var out []renamedName
	for _, stem := range operatorStems {
		if old := formerPrefix + "_" + stem; getenv(old) != "" {
			out = append(out, renamedName{old: old, now: productVariable(name, stem)})
		}
	}
	return out
}

// renamedRefusal is the start's refusal of the old variables it met, every
// one at once; nil when it met none.
func renamedRefusal(found []renamedName) error {
	if len(found) == 0 {
		return nil
	}
	diags := make([]model.Diagnostic, 0, len(found))
	for _, r := range found {
		diags = append(diags, diagnosticOf("error", "", nil, say("config.renamed", "old", r.old, "new", r.now)))
	}
	return &DiagnosticsError{Diagnostics: diags, Renamed: renamedList(found)}
}

// renamedList is the refusal's Renamed: every name once, sorted; nil for
// none.
func renamedList(found []renamedName) []string {
	if len(found) == 0 {
		return nil
	}
	out := make([]string, 0, len(found))
	for _, r := range found {
		out = append(out, r.String())
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// renamedTag is what a field still tagged kit:"…" says once renamed:
// nothing when the field has no such tag.
func renamedTag(owner reflect.Type, sf reflect.StructField) (fieldRenamed, bool) {
	value, ok := sf.Tag.Lookup(formerTagKey)
	if !ok {
		return fieldRenamed{}, false
	}
	label := fieldLabel(owner, sf.Name)
	return fieldRenamed{
		field: declaredField{owner: owner, index: sf.Index[len(sf.Index)-1]},
		name:  renamedName{old: label + " " + formerTagKey + `:"` + value + `"`, now: tagKey + `:"` + value + `"`},
		said:  say("classify.tag-renamed", "field", label, "value", value),
	}, true
}

// fieldRenamed is one declared field still tagged kit:"…": the name the
// refusal lists and the sentence it says.
type fieldRenamed struct {
	field declaredField
	name  renamedName
	said  phrase
}

// uniqueRenamed keeps the first of each declared field.
func uniqueRenamed(in []fieldRenamed) []fieldRenamed {
	seen := map[declaredField]bool{}
	out := in[:0:0]
	for _, r := range in {
		if !seen[r.field] {
			seen[r.field] = true
			out = append(out, r)
		}
	}
	return out
}

// renamedTags are the fields still tagged kit:"…" in the types the app's
// declarations show or keep, each declared field once.
func (a *App) renamedTags() []renamedName {
	var out []renamedName
	seen := map[reflect.Type]bool{}
	for _, svc := range a.services {
		if svc == nil {
			continue
		}
		nodes, _ := svc.snapshot()
		for _, n := range nodes {
			if tn, ok := n.(typeSource); ok {
				out = append(out, renamedIn(tn.dataTypes(), seen)...)
			}
		}
	}
	return out
}

// renamedIn are the fields still tagged kit:"…" in types, each type not
// in seen once, which it adds to seen.
func renamedIn(types []dataType, seen map[reflect.Type]bool) []renamedName {
	var out []renamedName
	for _, d := range types {
		if d.typ == nil || seen[d.typ] {
			continue
		}
		seen[d.typ] = true
		for _, r := range planOf(d.typ).renamed {
			out = append(out, r.name)
		}
	}
	return out
}

// Unwrap is what the refusal is, when the framework has a code for it: with
// packages in Missing, an error carrying [CodeSubsystemMissing] (ADR
// 0179 §6); with names in Renamed, one carrying [CodeRenamed] (ADR 0180
// §3); both joined when the start found both. errs.HasCode reads either
// through the refusal; nil when it found neither.
func (e *DiagnosticsError) Unwrap() error {
	var missing, renamed error
	if len(e.Missing) > 0 {
		list := strings.Join(e.Missing, ", ")
		missing = errs.New(CodeSubsystemMissing, "SUBSYSTEM_NOT_IMPORTED", subsystemPublic,
			"app: the product declares what needs a subsystem whose registration package the program does not import: "+list,
			errs.String("packages", list))
	}
	if len(e.Renamed) > 0 {
		list := strings.Join(e.Renamed, ", ")
		renamed = errs.New(CodeRenamed, "OLD_NAME", renamedPublic,
			"app: the start met names the framework no longer reads: "+list,
			errs.String("renamed", list))
	}
	switch {
	case missing != nil && renamed != nil:
		return errors.Join(missing, renamed)
	case missing != nil:
		return missing
	default:
		return renamed
	}
}
