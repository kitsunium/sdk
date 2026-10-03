// Package kit — settings: a value the environment gives a service.
package kit

import (
	"fmt"
	"io/fs"
	"maps"
	"math"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	sdkconfig "github.com/kitsunium/sdk/pkg/v1/config"
	jsoncodec "github.com/kitsunium/sdk/pkg/v1/data/codec/json"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/security/redact"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// The kinds of a setting.
const (
	settingText     = "text"
	settingBool     = "bool"
	settingInt      = "int"
	settingNumber   = "number"
	settingDuration = "duration"
	settingList     = "list"
)

// maxExactFloat is the magnitude below which every integer has an exact
// float64, and no other integer reads as it.
const maxExactFloat float64 = 1 << 53

// configDir is where ConfigFiles looks, and configBase the file every
// environment reads.
const (
	configDir  = "config"
	configBase = "config"
)

// The subsystem packages that read YAML and TOML configuration files.
const (
	configYAMLPackage = "github.com/kitsunium/sdk/framework/kit/config/yaml"
	configTOMLPackage = "github.com/kitsunium/sdk/framework/kit/config/toml"
)

// maxSettingText bounds a setting's value as the configuration shows it.
const maxSettingText int = 512

var (
	// configExtensions are the extensions a configuration file may have, and
	// the subsystem package that reads each but JSON, which kit reads itself:
	// a product links the YAML and TOML codecs only when it imports theirs.
	configExtensions = []struct{ ext, pkg string }{
		{"json", ""}, {"yaml", configYAMLPackage}, {"yml", configYAMLPackage}, {"toml", configTOMLPackage},
	}

	// configFormats are the SDK codecs configuration files are read with, by
	// extension: JSON's, and those RegisterConfigFormat added.
	configFormats sync.Map

	// serviceLink matches, by name alone, the variables Kubernetes and Docker
	// links give a process for every service it can reach: TODO_DB_SERVICE_HOST,
	// TODO_DB_SERVICE_PORT_HTTP, TODO_DB_PORT_5432_TCP_ADDR.
	serviceLink = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`_(SERVICE_HOST|SERVICE_PORT(_[A-Z0-9_]+)?|PORT_[0-9]+_(TCP|UDP|SCTP)(_(PROTO|PORT|ADDR))?)$`)
	})

	// envNameGrammar is what an environment name must be to name a file.
	envNameGrammar = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`) })

	// textParsers read a setting's value from a variable's trimmed text, by the
	// setting's type; a text setting keeps the text as it is, and a list splits
	// it on commas.
	textParsers = map[reflect.Type]func(text string) (any, error){
		reflect.TypeFor[bool](): func(text string) (any, error) {
			b, err := strconv.ParseBool(text)
			if err != nil {
				return nil, valueProblem("value.bool")
			}
			return b, nil
		},
		reflect.TypeFor[int](): func(text string) (any, error) {
			n, err := strconv.ParseInt(text, 10, strconv.IntSize)
			if err != nil {
				return nil, valueProblem("value.int")
			}
			return int(n), nil
		},
		reflect.TypeFor[int64](): func(text string) (any, error) {
			n, err := strconv.ParseInt(text, 10, 64)
			if err != nil {
				return nil, valueProblem("value.int")
			}
			return n, nil
		},
		reflect.TypeFor[float64](): func(text string) (any, error) {
			f, err := strconv.ParseFloat(text, 64)
			if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
				return nil, valueProblem("value.number")
			}
			return f, nil
		},
		reflect.TypeFor[time.Duration](): func(text string) (any, error) {
			d, err := time.ParseDuration(text)
			if err != nil {
				return nil, valueProblem("value.duration")
			}
			return d, nil
		},
	}

	// fileReaders read a value a configuration file writes, by the setting's
	// type; a text or a duration is read as a variable's text, a list as texts.
	fileReaders = map[reflect.Type]func(v fileValue) (settingValue, error){
		reflect.TypeFor[bool](): func(v fileValue) (settingValue, error) {
			b, ok := v.raw.(bool)
			if !ok {
				return settingValue{}, valueProblem("value.bool")
			}
			return settingValue{b}, nil
		},
		reflect.TypeFor[int](): fileInt,
		reflect.TypeFor[int64](): func(v fileValue) (settingValue, error) {
			n, ok := wholeNumber(v)
			if !ok {
				return settingValue{}, valueProblem("value.int")
			}
			return settingValue{n}, nil
		},
		reflect.TypeFor[float64](): fileNumber,
	}

	// wholeKinds are the integer types a configuration file's value may be read
	// as, by how their value reads: as a signed or an unsigned integer.
	wholeKinds = map[reflect.Type]reflect.Kind{
		reflect.TypeFor[int]():    reflect.Int64,
		reflect.TypeFor[int8]():   reflect.Int64,
		reflect.TypeFor[int16]():  reflect.Int64,
		reflect.TypeFor[int32]():  reflect.Int64,
		reflect.TypeFor[int64]():  reflect.Int64,
		reflect.TypeFor[uint8]():  reflect.Uint64,
		reflect.TypeFor[uint16](): reflect.Uint64,
		reflect.TypeFor[uint32](): reflect.Uint64,
		reflect.TypeFor[uint]():   reflect.Uint64,
		reflect.TypeFor[uint64](): reflect.Uint64,
	}
)

// Settings: the values an environment gives a product — a base URL, a delay,
// a limit — each declared by the service that uses it, with its default. kit
// resolves them when the app starts, lowest to highest: the default, the
// configuration file every environment reads (config/config.<ext>), the
// environment's own (config/<env>.<ext>), the variable <APP>_<NAME>, and the
// code ([Set], for tests). The files are the product's, committed and
// embedded in its binary ([ConfigFiles]), read through the SDK's
// config.FSSource; a file never holds a secret. A value holds for the run: a
// new one takes a restart. ADR 0003.

// SettingService is a value the environment gives a service, declared with
// [Service.Setting].
type SettingService[T SettingValue] struct {
	settingBase
	def T
}

// settingBase is what kit knows of a setting, whatever it holds.
type settingBase struct {
	// svc declares it; nil for one kit declares for a database, which
	// database names.
	svc      *Service
	database string
	name, id string
	decl     pos
	required bool
	// waitedBy are the timers that wait a duration setting: it must be
	// positive.
	waitedBy []phrase
	// check judges a resolved value: the problem it has, or nothing.
	check func(v any) phrase
}

type settingOption func(o *settingBase)

// errSettingValue is a value that is not of the setting's kind. It never
// quotes the value: the phrase says what was expected.
type errSettingValue struct{ said phrase }

// resolvedSettings is what the start made of the settings.
type resolvedSettings struct {
	// values are by setting ID.
	values map[string]any
	// shown are the settings as runtime.config says them.
	shown    []model.Setting
	problems []model.Diagnostic
}

// configFile is one configuration file the app reads: its codec, "" while
// pkg — the subsystem package that reads it — is not imported.
type configFile struct{ path, format, pkg string }

// settingFound is a setting's value, and where it came from.
type settingFound struct {
	value        settingValue
	from, detail string
}

// settingValue is a setting's value, of the type the setting declares — one
// of SettingValue's —, as the declarations hand it around: their type
// parameter is not one a resolution over every setting can name.
type settingValue struct{ held any }

// fileValue is what a configuration file writes for a setting, as its
// decoder read it: a string, a bool, a number, or a list.
type fileValue struct{ raw any }

// settingResolution is one resolution of the app's settings: what each
// layer gave, and the problems it met.
type settingResolution struct {
	a        *App
	byName   map[string]settingDecl
	secrets  map[string]bool
	prefix   string
	winner   map[string]settingFound
	resolved resolvedSettings
}

// owner is the service that declares the setting; "" for kit's own.
func (b *settingBase) owner() string {
	if b.svc == nil {
		return ""
	}
	return b.svc.name
}

// settingConfigure sets the option on what it configures.
func (f settingOption) settingConfigure(o *settingBase) { f(o) }

// Required drops a setting's default: a start without a value for it fails,
// saying where to give one.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Required() SettingConfigurer {
	return settingOption(func(o *settingBase) { o.required = true })
}

// Setting declares a setting of the service: name holds def unless the
// environment says otherwise — the variable <APP>_<NAME>, TODO_BASE_URL for
// the setting "base-url" of the app "todo", or the key name in the product's
// configuration files. [SettingService.Get] reads it.
//
// name follows the secrets' grammar — 1 to 63 lower-case letters, digits and
// dashes, starting and ending with a letter or a digit — and is unique in the
// app, among its settings and its secrets: the two share their variables.
//
//go:noinline
func (s *Service) Setting[T SettingValue](name string, def T, opts ...SettingConfigurer) *SettingService[T] {
	st := NewSettingService(def)
	st.svc, st.name, st.decl = s, name, callerPos()
	st.id = s.name + "/setting/" + name
	for _, o := range opts {
		if o != nil {
			o.settingConfigure(&st.settingBase)
		}
	}
	switch {
	case secret.ValidateName(name) != nil:
		s.problem(st.decl, s.name, "setting.name", "name", name, "max", secret.MaxNameLen)
	case strings.HasSuffix(name, "-file"):
		s.problem(st.decl, s.name, "setting.file-suffix", "name", name)
	case strings.HasPrefix(name, kitSecretPrefix):
		s.problem(st.decl, s.name, "setting.kit-prefix", "name", name, "prefix", kitSecretPrefix)
	}
	s.mu.Lock()
	s.settings = append(s.settings, st)
	s.mu.Unlock()
	return st
}

// Get returns the setting's value in the app the service is mounted in —
// its default until an app has started with it.
func (s *SettingService[T]) Get() T {
	if a := s.svc.app.Load(); a != nil {
		if values := a.settingValues.Load(); values != nil {
			if t, ok := (*values)[s.id].(T); ok {
				return cloneSetting(t)
			}
		}
	}
	return cloneSetting(s.def)
}

// Name is the setting's name, its key in a configuration file — in its
// module's section, for a module's setting.
func (s *SettingService[T]) Name() string { return s.name }

// key is the setting's name as a product says it — in a configuration
// file, to kit.Set, in its variable: "<module>.<name>" for a module's
// (ADR 0008), its name for the product's own.
func (b *settingBase) key() string { return qualifiedKey(b.svc, b.name) }

// cloneSetting copies a list, which a caller could otherwise change for
// everyone.
func cloneSetting[T SettingValue](v T) T {
	if l, ok := any(v).([]string); ok {
		if c, isT := any(slices.Clone(l)).(T); isT {
			return c
		}
	}
	return v
}

// base is what every setting shares.
func (s *SettingService[T]) base() *settingBase { return &s.settingBase }

// defaultValue is the value the setting holds when nothing sets it.
func (s *SettingService[T]) defaultValue() settingValue { return settingValue{s.def} }

// kind is the setting's type, as the model names it.
func (s *SettingService[T]) kind() string {
	switch any(s.def).(type) {
	case string:
		return settingText
	case bool:
		return settingBool
	case int, int64:
		return settingInt
	case float64:
		return settingNumber
	case time.Duration:
		return settingDuration
	}
	return settingList
}

// Error is the sentence that refused the value.
func (e errSettingValue) Error() string { return e.said.String() }

// valueProblem is a refused value's phrase.
func valueProblem(key string, args ...any) error { return errSettingValue{said: say(key, args...)} }

// saidOf is what err says: its phrase when it is a refused value.
func saidOf(err error) phrase {
	if e, ok := err.(errSettingValue); ok {
		return e.said
	}
	return plain(err.Error())
}

// fromText reads the setting's value from the text of a variable.
func (s *SettingService[T]) fromText(text string) (settingValue, error) {
	if parse, ok := textParsers[reflect.TypeFor[T]()]; ok {
		v, err := parse(strings.TrimSpace(text))
		return settingValue{v}, err
	}
	if _, isText := any(s.def).(string); isText {
		return settingValue{text}, nil
	}
	var list []string
	for item := range strings.SplitSeq(text, ",") {
		if item = strings.TrimSpace(item); item != "" {
			list = append(list, item)
		}
	}
	return settingValue{list}, nil
}

// fromFile reads the setting's value from what a config file holds.
func (s *SettingService[T]) fromFile(v fileValue) (settingValue, error) {
	switch any(s.def).(type) {
	case string:
		return s.fileText(v, func() error { return valueProblem("value.text") })
	case time.Duration:
		return s.fileText(v, func() error { return valueProblem("value.duration-text") })
	default:
		if read, ok := fileReaders[reflect.TypeFor[T]()]; ok {
			return read(v)
		}
		return fileList(v)
	}
}

// fileText reads a text a file writes — a duration included — refusing
// anything else with the problem refused says.
func (s *SettingService[T]) fileText(v fileValue, refused func() error) (settingValue, error) {
	text, ok := v.raw.(string)
	if !ok {
		return settingValue{}, refused()
	}
	return s.fromText(text)
}

// fileInt reads an int a file writes, within an int's range.
func fileInt(v fileValue) (settingValue, error) {
	n, ok := wholeNumber(v)
	switch {
	case !ok:
		return settingValue{}, valueProblem("value.int")
	case n < math.MinInt || n > math.MaxInt:
		return settingValue{}, valueProblem("value.range")
	default:
		return settingValue{int(n)}, nil
	}
}

// fileNumber reads a number a file writes: a whole one, or a finite float.
func fileNumber(v fileValue) (settingValue, error) {
	if n, ok := wholeNumber(v); ok {
		return settingValue{float64(n)}, nil
	}
	f, ok := v.raw.(float64)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return settingValue{}, valueProblem("value.number")
	}
	return settingValue{f}, nil
}

// fileList reads a list of texts a file writes.
func fileList(v fileValue) (settingValue, error) {
	items, ok := v.raw.([]any)
	if !ok {
		return settingValue{}, valueProblem("value.list")
	}
	list := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			return settingValue{}, valueProblem("value.list")
		}
		list = append(list, text)
	}
	return settingValue{list}, nil
}

// wholeNumber reads an integer a decoder produced, of whatever Go type.
func wholeNumber(v fileValue) (int64, bool) {
	if f, isFloat := v.raw.(float64); isFloat {
		// A JSON number arrives as a float64, exact only below 2^53: from
		// it on, the integer the file wrote may not be the one read — 2^53+1
		// reads as 2^53.
		if f == math.Trunc(f) && math.Abs(f) < maxExactFloat {
			return int64(f), true
		}
		return 0, false
	}
	rv := reflect.ValueOf(v.raw)
	if !rv.IsValid() {
		return 0, false
	}
	switch kind, known := wholeKinds[rv.Type()]; {
	case !known:
		return 0, false
	case kind == reflect.Int64:
		return rv.Int(), true
	default:
		u := rv.Uint()
		return int64(u), u <= math.MaxInt64
	}
}

// fromCode reads the setting's value from what the code sets.
func (s *SettingService[T]) fromCode(v any) (settingValue, error) {
	if t, ok := v.(T); ok {
		return settingValue{cloneSetting(t)}, nil
	}
	if text, ok := v.(string); ok {
		return s.fromText(text)
	}
	return settingValue{}, valueProblem("value.type", "got", fmt.Sprintf("%T", v), "kind", s.kind())
}

// text spells the value v as a variable would hold it.
func (s *SettingService[T]) text(v settingValue) string {
	switch x := v.held.(type) {
	case string:
		return x
	case []string:
		return strings.Join(x, ",")
	case time.Duration:
		return x.String()
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	}
	return fmt.Sprint(v.held)
}

// Set gives the setting name a value in code, over the environment and the
// files: a value of the setting's type, or its text as a variable would
// hold it. It is for tests.
func Set(name string, value any) AppConfigurer {
	return appOption(func(o *appOptions) {
		if o.settings == nil {
			o.settings = map[string]any{}
		}
		o.settings[name] = value
	})
}

// ConfigFiles gives kit the product's configuration files: config/config.<ext>,
// which every environment reads, and config/<env>.<ext>, the environment's
// own — <env> being KIT_ENV as written, dev when kit dev runs the product —
// in JSON, YAML or TOML (json, yaml or yml, toml). A file maps the names of
// the settings to their values; it never holds a secret, and a key no
// service declares stops the start. The files belong in the binary:
//
//	//go:embed config
//	var configFiles embed.FS
//
//	app := kit.NewApp("todo", ...).With(kit.ConfigFiles(configFiles))
func ConfigFiles(fsys fs.FS) AppConfigurer {
	return appOption(func(o *appOptions) { o.configFiles = fsys })
}

// resolveSettings reads every declared setting from its sources, and says
// every problem at once: a setting two services declare, a required one no
// source gives, a value of the wrong kind, a file key no service declares or
// that names a secret.
func (a *App) resolveSettings() resolvedSettings {
	r := &settingResolution{a: a, resolved: resolvedSettings{values: map[string]any{}}, winner: map[string]settingFound{}}
	decls, byName := a.declaredSettings(r.problem)
	r.byName, r.secrets, r.prefix = byName, a.declaredSecretNames(), appPrefix(a.name)
	// The layers, lowest first: a value and where it came from, by name.
	for _, file := range a.configFiles(r.problem) {
		r.fromFile(file)
	}
	r.resolved.problems = append(r.resolved.problems, strayVariables(r.prefix, decls, r.secrets)...)
	for _, d := range decls {
		r.fromEnv(d)
	}
	for _, name := range slices.Sorted(maps.Keys(a.opts.settings)) {
		r.fromCode(name)
	}
	for _, d := range decls {
		r.settle(d)
	}
	return r.resolved
}

// problem records a problem of the resolution, at a declaration when at
// names one.
func (r *settingResolution) problem(node string, at *pos, key string, args ...any) {
	var src *model.Source
	if at != nil {
		src = r.a.source(at)
	}
	r.resolved.problems = append(r.resolved.problems, diagnosticOf("error", node, src, say(key, args...)))
}

// fromFile takes the values a configuration file gives.
func (r *settingResolution) fromFile(file configFile) {
	if file.format == "" {
		r.problem("", nil, "setting.file-format", "file", file.path, "package", file.pkg)
		return
	}
	values, err := sdkconfig.FSSource(r.a.opts.configFiles, file.format, file.path).Load()
	if err != nil {
		r.problem("", nil, "setting.file-unreadable", "file", file.path, "detail", errs.PublicOf(err))
		return
	}
	values = r.a.moduleSections(file.path, values, r.problem)
	for _, key := range slices.Sorted(maps.Keys(values)) {
		d, declared := r.byName[key]
		switch {
		case r.secrets[key]:
			r.problem("", nil, "setting.file-secret", "file", file.path, "key", key, "variable", variableOf(r.prefix, key), "app", r.a.name)
		case !declared:
			r.problem("", nil, "setting.file-unknown", "file", file.path, "key", key)
		default:
			v, err := d.fromFile(fileValue{values[key]})
			if err != nil {
				r.problem(d.base().owner(), &d.base().decl, "setting.file-value", "file", file.path, "key", key, "problem", saidOf(err))
				continue
			}
			r.winner[key] = settingFound{value: v, from: model.SettingFile, detail: file.path}
		}
	}
}

// fromEnv takes the value the environment gives d.
func (r *settingResolution) fromEnv(d settingDecl) {
	b := d.base()
	variable := variableOf(r.prefix, b.key())
	text, ok := os.LookupEnv(variable)
	if !ok || text == "" {
		return
	}
	v, err := d.fromText(text)
	if err != nil {
		r.problem(b.owner(), &b.decl, "setting.env-value", "variable", variable, "problem", saidOf(err))
		return
	}
	r.winner[b.key()] = settingFound{value: v, from: model.SettingEnv, detail: variable}
}

// fromCode takes the value kit.Set gives the setting name.
func (r *settingResolution) fromCode(name string) {
	d, declared := r.byName[name]
	if !declared {
		r.problem("", nil, "setting.code-unknown", "key", name)
		return
	}
	v, err := d.fromCode(r.a.opts.settings[name])
	if err != nil {
		r.problem(d.base().owner(), &d.base().decl, "setting.code-value", "key", name, "problem", saidOf(err))
		return
	}
	r.winner[name] = settingFound{value: v, from: model.SettingOption, detail: "kit.Set"}
}

// settle decides d's value — the winning layer's, else its default — checks
// it, and keeps it with what the config shows of it.
func (r *settingResolution) settle(d settingDecl) {
	b := d.base()
	variable := variableOf(r.prefix, b.key())
	w, ok := r.winner[b.key()]
	switch {
	case ok:
	case b.required:
		r.problem(b.owner(), &b.decl, "setting.required", "key", b.key(), "service", b.owner(), "variable", variable, "file", r.a.configFileHint())
		return
	default:
		w = settingFound{value: d.defaultValue(), from: model.SettingDefault}
	}
	if !r.valid(d, variable, w.value) {
		return
	}
	r.resolved.values[b.id] = w.value.held
	s := model.Setting{Name: variable, Service: b.owner(), Database: b.database, Key: b.key(), Type: d.kind(), From: w.from, Value: shownValue(d, b.name, w.value)}
	switch w.from {
	case model.SettingFile:
		s.Detail = w.detail
	case model.SettingOption:
		s.Option = w.detail
	}
	r.resolved.shown = append(r.resolved.shown, s)
}

// valid checks a setting's value: a positive duration where a workflow
// waits on it, and what the declaration's own check says.
func (r *settingResolution) valid(d settingDecl, variable string, value settingValue) bool {
	b := d.base()
	if dur, ok := value.held.(time.Duration); ok && dur <= 0 && len(b.waitedBy) > 0 {
		r.problem(b.owner(), &b.decl, "setting.positive", "key", b.key(), "waiters", listOf(b.waitedBy))
		return false
	}
	if b.check == nil {
		return true
	}
	if p := b.check(value.held); !p.empty() {
		r.problem(b.owner(), &b.decl, "setting.invalid", "key", b.key(), "variable", variable, "problem", p)
		return false
	}
	return true
}

// platformVariable reports whether a variable under the product's prefix is
// one a platform gives every process — named after a service it can reach,
// which shares the prefix when a service is named like the product — rather
// than one for the product: the names serviceLink matches, TODO_DB_PORT
// holding tcp://…, and Docker's TODO_DB_NAME=/app/db and TODO_DB_ENV_<VAR>.
// A TODO_SMTP_PORT holding a number is the product's, and a typo.
func platformVariable(name, value string) bool {
	switch {
	case serviceLink().MatchString(name):
		return true
	case strings.HasSuffix(name, "_PORT"):
		return strings.HasPrefix(value, "tcp://") || strings.HasPrefix(value, "udp://") || strings.HasPrefix(value, "sctp://")
	case strings.HasSuffix(name, "_NAME"):
		return strings.HasPrefix(value, "/")
	}
	return strings.Contains(name, "_ENV_")
}

// strayVariables warns of each variable under the product's prefix that no
// setting or secret reads: a typo, most often, which would otherwise be
// ignored. A warning and not a refusal: the platform a product runs on may
// set variables of its own under the same prefix.
func strayVariables(prefix string, decls []settingDecl, secrets map[string]bool) []model.Diagnostic {
	known := map[string]bool{}
	for _, d := range decls {
		known[variableOf(prefix, d.base().key())] = true
	}
	for name := range secrets {
		v := variableOf(prefix, name)
		known[v], known[v+"_FILE"] = true, true
	}
	var stray []string
	for _, kv := range os.Environ() {
		name, value, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, prefix+"_") && !known[name] && !platformVariable(name, value) {
			stray = append(stray, name)
		}
	}
	slices.Sort(stray)
	out := make([]model.Diagnostic, 0, len(stray))
	for _, name := range slices.Compact(stray) {
		out = append(out, diagnosticOf("warning", "", nil, say("setting.stray", "variable", name)))
	}
	return out
}

// shownValue is a setting's value as the configuration shows it: its text,
// the credentials of a URL replaced — and, for a text or a list whose name
// reads like a secret's (a token, a password…), not at all: a secret belongs
// in Service.Secret, but one put in a setting is still not shown.
func shownValue(d settingDecl, name string, v settingValue) string {
	if k := d.kind(); (k == settingText || k == settingList) && redactor.Name(name) {
		return redact.Placeholder
	}
	return redactor.Text(d.text(v), maxSettingText)
}

// declaredSettings lists the settings of the mounted services, in the order
// of the services, then those kit declares for the app's databases, and by
// key — qualified with its module's name for a module's service's (ADR
// 0008).
func (a *App) declaredSettings(problem func(node string, at *pos, key string, args ...any)) ([]settingDecl, map[string]settingDecl) {
	decls, byName := a.serviceSettings(problem)
	for _, d := range a.databaseSettings(byName) {
		byName[d.base().name] = d
		decls = append(decls, d)
	}
	return decls, byName
}

// serviceSettings are the settings the mounted services declare, and by
// key: a key two services declare, or a variable two keys or a key and a
// secret share, is a problem, and a malformed name was said when it was
// declared. A name a database takes is the database's problem
// (databaseProblems): the service's setting is left out.
func (a *App) serviceSettings(problem func(node string, at *pos, key string, args ...any)) ([]settingDecl, map[string]settingDecl) {
	var decls []settingDecl
	byName, byVariable := map[string]settingDecl{}, map[string]settingDecl{}
	secrets := map[string]bool{}
	for key := range a.declaredSecretNames() {
		secrets[flatKey(key)] = true
	}
	derived := a.derivedNames()
	for _, d := range a.settingDecls() {
		b := d.base()
		if !settingNamed(b) || derived[flatKey(b.key())] || a.settingCollides(d, byVariable, secrets, problem) {
			continue
		}
		byName[b.key()], byVariable[flatKey(b.key())] = d, d
		decls = append(decls, d)
	}
	return decls, byName
}

// settingDecls are every setting the app's services declare, in order.
func (a *App) settingDecls() []settingDecl {
	var out []settingDecl
	for _, svc := range a.services {
		if svc == nil {
			continue
		}
		svc.mu.Lock()
		out = append(out, svc.settings...)
		svc.mu.Unlock()
	}
	return out
}

// settingNamed reports whether a setting's name is one a setting may take:
// a secret's grammar, not a file variable's, not kit's own.
func settingNamed(b *settingBase) bool {
	return secret.ValidateName(b.name) == nil && !strings.HasSuffix(b.name, "-file") && !strings.HasPrefix(b.name, kitSecretPrefix)
}

// databaseSettings are the settings kit declares for the app's databases,
// but for one whose name is taken already: the database's problem.
func (a *App) databaseSettings(byName map[string]settingDecl) []settingDecl {
	var out []settingDecl
	taken := a.derivedOwners()
	for _, db := range a.opts.databases {
		for _, d := range db.settingDecls() {
			b := d.base()
			if _, dup := byName[b.name]; dup || taken[b.name] != "" || secret.ValidateName(b.name) != nil {
				continue
			}
			out = append(out, d)
		}
	}
	return out
}

// settingCollides says the problem of a setting whose key another declares,
// or whose variable another setting's key or a secret's shares — the
// product's "moderation-tdb-url" and a module's "moderation.tdb-url" are
// both SHOP_MODERATION_TDB_URL —, and reports whether it has one.
func (a *App) settingCollides(d settingDecl, byVariable map[string]settingDecl, secrets map[string]bool, problem func(node string, at *pos, key string, args ...any)) bool {
	b := d.base()
	key, flat := b.key(), flatKey(b.key())
	variable := variableOf(appPrefix(a.name), key)
	prev, dup := byVariable[flat]
	switch {
	case dup && prev.base().key() == key:
		problem(b.svc.name, &b.decl, "setting.twice", "key", key, "first", prev.base().svc.name, "second", b.svc.name)
	case dup:
		problem(b.svc.name, &b.decl, "setting.variable-twice", "key", key, "second", b.svc.name,
			"other", prev.base().key(), "first", prev.base().svc.name, "variable", variable)
	case secrets[flat]:
		problem(b.svc.name, &b.decl, "setting.secret-name", "key", key, "variable", variable)
	default:
		return false
	}
	return true
}

// declaredSecretNames are the keys of the secrets the product declares —
// qualified with its module's name for a module's service's —, and the
// names of its databases' URLs.
func (a *App) declaredSecretNames() map[string]bool {
	names := map[string]bool{}
	for _, d := range a.opts.databases {
		names[d.urlSecret()] = true
	}
	for _, svc := range a.services {
		if svc == nil {
			continue
		}
		nodes, _ := svc.snapshot()
		for _, n := range nodes {
			if s, ok := n.(*Secret); ok && !s.kitOwn() {
				names[s.key()] = true
			}
		}
	}
	return names
}

// moduleSections reads a configuration file's sections of the modules the
// app mounts — "moderation:" and its settings' names under it — as the
// settings' qualified keys, "moderation.tdb-url". A section that is not a
// map, or a qualified key outside its section, is said: a module's settings
// are set in its section only.
func (a *App) moduleSections(file string, values map[string]any, problem func(node string, at *pos, key string, args ...any)) map[string]any {
	if len(a.modules) == 0 {
		return values
	}
	out := make(map[string]any, len(values))
	for key, v := range values {
		section, isMap := v.(map[string]any)
		switch {
		case a.mountsModule(key) && isMap:
			for name, value := range section {
				out[key+"."+name] = value
			}
		case a.mountsModule(key):
			problem("", nil, "setting.file-section", "file", file, "module", key)
		case strings.Contains(key, "."):
			problem("", nil, "setting.file-unknown", "file", file, "key", key)
		default:
			out[key] = v
		}
	}
	return out
}

// mountsModule reports whether the app mounts a module named name.
func (a *App) mountsModule(name string) bool {
	for _, mm := range a.modules {
		if mm.module.name == name {
			return true
		}
	}
	return false
}

// configFiles are the files the environment reads, lowest first: at most one
// config/config.<ext>, then at most one config/<env>.<ext>.
func (a *App) configFiles(problem func(node string, at *pos, key string, args ...any)) []configFile {
	fsys := a.opts.configFiles
	if fsys == nil {
		return nil
	}
	env := a.cfg.envName
	if !envNameGrammar().MatchString(env) {
		problem("", nil, "setting.env-name", "env", env)
		return nil
	}
	bases := []string{configBase}
	if env != configBase {
		bases = append(bases, env)
	}
	var out []configFile
	for _, base := range bases {
		matches := configFilesOf(fsys, base)
		switch len(matches) {
		case 0:
		case 1:
			out = append(out, matches[0])
		default:
			problem("", nil, "setting.files", "files", pathsOf(matches))
		}
	}
	return out
}

// configFilesOf are the configuration files of base, in every format kit
// reads: more than one is a problem the caller says.
func configFilesOf(fsys fs.FS, base string) []configFile {
	var matches []configFile
	for _, e := range configExtensions {
		path := configDir + "/" + base + "." + e.ext
		if info, err := fs.Stat(fsys, path); err == nil && !info.IsDir() {
			format, _ := configFormat(e.ext)
			matches = append(matches, configFile{path: path, format: format, pkg: e.pkg})
		}
	}
	return matches
}

// configFormat is the codec a configuration file of extension ext is read
// with; found is false while the package that reads it is not imported.
func configFormat(ext string) (format string, found bool) {
	if ext == "json" {
		return jsoncodec.Format, true
	}
	f, found := configFormats.Load(ext)
	if !found {
		return "", false
	}
	format, found = f.(string)
	return format, found
}

// RegisterConfigFormat lets configuration files of extension ext be read
// with the SDK codec format. framework/kit/config/yaml and config/toml call
// it as they are imported; a product never does.
func RegisterConfigFormat(ext, format string) { configFormats.Store(ext, format) }

// pathsOf lists the files' paths.
func pathsOf(files []configFile) phrase {
	paths := make([]phrase, len(files))
	for i, m := range files {
		paths[i] = plain(m.path)
	}
	return listOf(paths)
}

// configFileHint names the file a value for the current environment goes in.
func (a *App) configFileHint() string {
	return configDir + "/" + a.cfg.envName + ".yaml"
}

// NewSettingService is a setting no service declares yet, holding def until
// something sets it: [Service.Setting] makes one and declares it, which is
// how a product gets one.
func NewSettingService[T SettingValue](def T) *SettingService[T] { return &SettingService[T]{def: def} }
