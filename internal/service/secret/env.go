// Package secret — the read-only store over the process environment, with the
// NAME_FILE convention Docker and Kubernetes secrets use.
package secret

import (
	"bytes"
	"context"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	coresecret "github.com/kitsunium/sdk/internal/core/secret"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// fileSuffix marks the variable naming a FILE whose content is the secret —
// the convention the official container images and Kubernetes manifests use,
// so that the value itself never sits in the environment where /proc, `docker
// inspect` and every child process can read it.
const fileSuffix string = "_FILE"

// maxEnvFileBytes bounds what a _FILE secret may hold. A secret is a password,
// a token, a key or a certificate — kilobytes at most. A file larger than this
// is almost certainly a wrong path (a binary, a log, a directory's worth of
// data), and reading it whole into memory to find out would be the failure.
const maxEnvFileBytes int64 = 64 << 10

// prefixSeparator joins a prefix to the variable part of a name.
const prefixSeparator string = "_"

var (
	// crlf is the line ending a Windows editor writes.
	crlf = []byte("\r\n")
	// newline is the line ending `echo` writes.
	newline = []byte("\n")
)

// EnvConfig parameterises [NewEnv].
type EnvConfig struct {
	// Prefix namespaces every variable: with "APP", the secret "smtp-url" is
	// read from APP_SMTP_URL, or from the file APP_SMTP_URL_FILE names. The
	// separating underscore belongs to the store, and a trailing one on Prefix
	// is absorbed, so "APP" and "APP_" name the same namespace.
	//
	// Empty is allowed and means no namespace: "smtp-url" reads SMTP_URL. It
	// is a legitimate choice — DATABASE_URL is a convention of its own — and
	// Names then lists the whole environment, mapped to names, which is noise
	// but never a value.
	//
	// A non-empty prefix must be uppercase ASCII letters, digits and '_', and
	// must not start with a digit or consist of underscores alone: anything
	// else is not a variable name a shell can export.
	Prefix string
}

// envStore reads secrets from the process environment and never writes.
type envStore struct {
	// prefix is the normalised namespace: "" or "NAME_".
	prefix string
}

// NewEnv returns a read-only Store over the process environment.
//
// The secret "smtp-url" under prefix "APP" is read from the variable
// APP_SMTP_URL, or — the Docker and Kubernetes convention — from the file the
// variable APP_SMTP_URL_FILE names, with ONE trailing line ending removed,
// because a file written with `echo` ends with one and a password does not.
// Setting both is refused with [EnvRefused] rather than resolved by a
// precedence rule, exactly as the official container images refuse it. An
// empty variable counts as unset, since shells and compose files export empty
// variables to mean exactly that; an empty FILE is refused, since a file named
// explicitly and found empty is a mount that did not populate.
//
// Every secret it returns is version 1: the environment carries one value and
// no history. Created is the file's modification time for the _FILE form and
// the zero time for a variable, which says when it was set to nobody.
//
// Put and Prune are refused with core/secret.ReadOnly: the process did not
// write its own environment and cannot rotate what an orchestrator mounted.
func NewEnv(cfg EnvConfig) (store coresecret.Store, err error) {
	prefix, prefixErr := normalisePrefix(cfg.Prefix)
	//: a prefix that is not a variable name is refused at construction.
	if prefixErr != nil {
		//: InvalidConfig, naming the setting.
		return nil, prefixErr
	}
	//: a stateless reader, safe to share.
	return envStore{prefix: prefix}, nil
}

// normalisePrefix validates a configured prefix and appends its separator.
func normalisePrefix(prefix string) (normalised string, err error) {
	//: no namespace at all is a legitimate choice.
	if prefix == "" {
		//: variables are the bare upper-cased names.
		return "", nil
	}
	name := strings.TrimRight(prefix, prefixSeparator)
	//: a prefix of underscores alone would silently collapse to no namespace.
	if name == "" || !isVariableName(name) {
		//: refused, naming the setting and the clause, never guessing.
		return "", wrapAs(InvalidConfig, nil, errs.String("setting", "Prefix"),
			errs.String("problem", "must be uppercase letters, digits and '_', and not start with a digit"))
	}
	//: the namespace, with the one separator the store supplies.
	return name + prefixSeparator, nil
}

// isVariableName reports whether name is spelled the way a POSIX shell exports
// a variable, in upper case: letters, digits and '_', not starting with a
// digit.
func isVariableName(name string) bool {
	//: every byte from the closed alphabet.
	for index := range len(name) {
		b := name[index]
		letter := (b >= 'A' && b <= 'Z') || b == '_'
		digit := b >= '0' && b <= '9'
		//: a digit may not lead; anything outside the alphabet may not appear.
		if !letter && (!digit || index == 0) {
			//: not a variable name.
			return false
		}
	}
	//: a variable name.
	return true
}

// variableFor maps a valid secret name to the variable that carries it:
// upper-cased, '-' turned into '_', behind the prefix. The mapping is
// one-to-one because '_' is not in the name alphabet.
func (e envStore) variableFor(name string) string {
	//: the name alphabet is lowercase ASCII, digits and '-', so this is exact.
	return e.prefix + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

// resolve validates name and returns the variable that carries it.
func (e envStore) resolve(name string) (variable string, err error) {
	//: a malformed name is refused before the environment is read.
	if nameErr := coresecret.ValidateName(name); nameErr != nil {
		//: InvalidName.
		return "", nameErr
	}
	variable = e.variableFor(name)
	//: a name whose variable already ends in _FILE cannot be told apart from
	//: the file form of a shorter name, so this store refuses it rather than
	//: reading one of the two.
	if strings.HasSuffix(variable, fileSuffix) {
		//: InvalidName, with the clause this store adds to the grammar.
		return "", errs.Wrap(coresecret.InvalidName, errs.WrapParams{},
			errs.String("problem", "a name ending in -file collides with the _FILE convention in the environment store"))
	}
	//: the variable to read.
	return variable, nil
}

// Get reads name from its variable or from the file its _FILE form names.
//
// The context is unused and named so: reading the environment does not block,
// and the one file read is bounded by maxEnvFileBytes.
func (e envStore) Get(_ context.Context, name string) (current coresecret.VersionValue, err error) {
	variable, resolveErr := e.resolve(name)
	//: a malformed or ambiguous name never reaches the environment.
	if resolveErr != nil {
		//: InvalidName.
		return coresecret.VersionValue{}, resolveErr
	}
	value := os.Getenv(variable)
	path := os.Getenv(variable + fileSuffix)
	//: an empty variable counts as unset, which is what shells mean by it.
	switch {
	//: both forms set: the operator meant one of them, and picking is guessing.
	case value != "" && path != "":
		//: EnvRefused, naming both variables and neither value.
		return coresecret.VersionValue{}, wrapAs(EnvRefused, nil, errs.String("secret", name),
			errs.String("variable", variable), errs.String("file_variable", variable+fileSuffix))
	//: the value itself is in the environment.
	case value != "":
		//: version 1; nobody told the environment when the variable was set.
		return coresecret.VersionValue{Name: name, Version: 1, Value: coresecret.FromString(value)}, nil
	//: the value is in the file the variable names.
	case path != "":
		//: read, bounded, one line ending trimmed.
		return readSecretFile(name, variable+fileSuffix, path)
	//: neither form set.
	default:
		//: NotFound.
		return coresecret.VersionValue{}, notFound(name)
	}
}

// Versions returns the single version the environment carries.
func (e envStore) Versions(ctx context.Context, name string) (versions []coresecret.VersionValue, err error) {
	current, getErr := e.Get(ctx, name)
	//: every verdict Get reaches is Versions' verdict too.
	if getErr != nil {
		//: InvalidName, NotFound, EnvRefused or StoreUnavailable.
		return nil, getErr
	}
	//: the environment has no history: one version, the current one.
	return []coresecret.VersionValue{current}, nil
}

// Put is refused: the environment is read-only to the process it configures.
// Nothing about the arguments could make a write to it meaningful, so none of
// them is inspected — and the value is never touched.
func (e envStore) Put(_ context.Context, _ string, _ coresecret.Value) (created coresecret.VersionValue, err error) {
	//: ReadOnly, naming the store and the operation.
	return coresecret.VersionValue{}, errs.Wrap(coresecret.ReadOnly, errs.WrapParams{},
		errs.String("store", "env"), errs.String("operation", "put"))
}

// Prune is refused: the environment keeps exactly one version and the process
// cannot remove it.
func (e envStore) Prune(_ context.Context, _ string, _ int) error {
	//: ReadOnly, as for Put.
	return errs.Wrap(coresecret.ReadOnly, errs.WrapParams{},
		errs.String("store", "env"), errs.String("operation", "prune"))
}

// Names lists every secret name the environment supplies under the prefix, in
// either form, sorted. A variable that does not map back to a valid name —
// lowercase letters in it, a character outside the grammar — is not a secret
// this store could Get, and is not listed.
func (e envStore) Names(_ context.Context) (names []string, err error) {
	found := make(map[string]bool, initialSecrets)
	//: each environ entry is "KEY=VALUE"; only the key is ever read.
	for _, entry := range os.Environ() {
		//: the name a variable designates, if it designates one.
		if name, ok := e.nameOf(entry); ok {
			found[name] = true
		}
	}
	//: sorted, so two calls answer identically.
	return slices.Sorted(maps.Keys(found)), nil
}

// nameOf maps one environ entry back to the secret name it supplies, reporting
// false for an entry this store would never read.
func (e envStore) nameOf(entry string) (name string, ok bool) {
	key, _, _ := strings.Cut(entry, "=")
	rest, inNamespace := strings.CutPrefix(key, e.prefix)
	//: outside the namespace, or the bare prefix itself.
	if !inNamespace || rest == "" {
		//: not a secret of this store.
		return "", false
	}
	//: the file form names the same secret as the value form.
	rest = strings.TrimSuffix(rest, fileSuffix)
	name = strings.ToLower(strings.ReplaceAll(rest, "_", "-"))
	//: listed only when Get(name) would read exactly this variable — which
	//: excludes lowercase keys, and names outside the grammar or ending -file.
	if coresecret.ValidateName(name) != nil || e.variableFor(name) != e.prefix+rest ||
		strings.HasSuffix(e.variableFor(name), fileSuffix) {
		//: not a name this store can serve.
		return "", false
	}
	//: a secret this store supplies.
	return name, true
}

// readSecretFile reads a _FILE secret: bounded, one trailing line ending
// removed, empty refused. Created is the file's modification time — the one
// timestamp the environment store can honestly report.
func readSecretFile(name, fileVariable, path string) (current coresecret.VersionValue, err error) {
	content, modified, readErr := readBounded(path)
	//: unreadable: absent, forbidden, a directory — possibly a volume that is
	//: not mounted YET, so it is the retryable verdict. The path is not
	//: repeated; the variable that names it is.
	if readErr != nil {
		//: StoreUnavailable, naming the variable and the operation.
		return coresecret.VersionValue{}, errs.Wrap(coresecret.StoreUnavailable, errs.WrapParams{},
			errs.String("secret", name), errs.String("file_variable", fileVariable), errs.String("operation", "read"))
	}
	//: one line ending is the file's, not the secret's.
	content = trimLineEnding(content)
	//: an explicitly named file that is empty, or too large to be a secret.
	if len(content) == 0 || int64(len(content)) > maxEnvFileBytes {
		//: EnvRefused, naming the variable, never the content or the path.
		return coresecret.VersionValue{}, wrapAs(EnvRefused, nil, errs.String("secret", name),
			errs.String("file_variable", fileVariable), errs.String("problem", "the file is empty or larger than 64 KiB"))
	}
	//: version 1, stamped with the file's modification time.
	return coresecret.VersionValue{Name: name, Version: 1, Value: coresecret.NewValue(content), Created: modified}, nil
}

// readBounded reads at most maxEnvFileBytes+1 bytes of path — one more than
// allowed, so an oversized file is detected without being read whole.
func readBounded(path string) (content []byte, modified time.Time, err error) {
	file, openErr := os.Open(path)
	//: absent, forbidden, or not a path at all.
	if openErr != nil {
		//: the caller turns this into StoreUnavailable without the path.
		return nil, time.Time{}, openErr
	}
	//: a close failure is reported, because a descriptor that did not close is
	//: a leak worth knowing of — but it never replaces the read's own verdict.
	defer func() {
		//: only the first failure is the caller's to see.
		if closeErr := file.Close(); closeErr != nil && err == nil {
			clear(content)
			content, modified, err = nil, time.Time{}, closeErr
		}
	}()
	info, statErr := file.Stat()
	//: a stat that fails on an open descriptor.
	if statErr != nil {
		//: the caller does not repeat the message's path.
		return nil, time.Time{}, statErr
	}
	//: a directory opens fine and reads as an error on some platforms and as
	//: nothing on others; it is refused the same way everywhere.
	if info.IsDir() {
		//: the store's own verdict; the caller wraps it without the path.
		return nil, time.Time{}, coresecret.StoreUnavailable
	}
	content, err = io.ReadAll(io.LimitReader(file, maxEnvFileBytes+1))
	//: a read that failed part-way leaves nothing the caller may use.
	if err != nil {
		clear(content)
		//: the caller does not repeat the message's path.
		return nil, time.Time{}, err
	}
	//: the content and the one timestamp the file carries.
	return content, info.ModTime(), nil
}

// trimLineEnding removes ONE trailing "\n" or "\r\n". Only one: a secret that
// genuinely ends in a newline written by `printf 'x\n\n'` keeps the other.
func trimLineEnding(content []byte) []byte {
	//: CRLF first, so a Windows-written file loses both bytes.
	if trimmed, found := bytes.CutSuffix(content, crlf); found {
		//: one CRLF removed.
		return trimmed
	}
	//: then a bare LF.
	if trimmed, found := bytes.CutSuffix(content, newline); found {
		//: one LF removed.
		return trimmed
	}
	//: no line ending to remove.
	return content
}
