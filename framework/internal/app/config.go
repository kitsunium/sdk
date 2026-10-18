package app

import (
	"cmp"
	"context"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// The environments.
const (
	// EnvDev serves the Studio, analyzes the source and binds loopback. Only
	// `kit dev` or an explicit <PRODUCT>_ENV=dev turns it on.
	EnvDev = "dev"
	// EnvProduction is the default: no Studio, no source, no analysis.
	EnvProduction = "production"
)

var (
	// loopbackHosts are the names every app answers as, besides the ones
	// <PRODUCT>_ALLOWED_HOSTS adds.
	loopbackHosts = []string{"localhost", "127.0.0.1", "::1"}

	// devForcible are the variables kit dev may set itself for a launch; only
	// those can be labelled as its doing.
	devForcible = []string{model.VarEnv, model.VarAddr, model.VarDataDir}
)

// studioConfig is what the Studio may do: whether it is served at all,
// whether it answers a client that is not on this machine, and whether the
// static analysis runs for it.
type studioConfig struct {
	on, remote, analyze bool
}

// config is the resolved configuration of a running app.
type config struct {
	env string
	// envName is <PRODUCT>_ENV as written, lower-cased — production when it is
	// unset: it names the environment's configuration file, where env only
	// says dev or not.
	envName string
	addr    string
	dataDir string
	// memoryData says no data directory was given outside dev: what the app
	// keeps lives in memory — a warning when it keeps anything.
	memoryData bool
	// studio is what the Studio may do in this run.
	studio       studioConfig
	allowedHosts []string
	logLevel     string
	logs         io.Writer
	clock        clock.Timed
	warnings     []phrase
	// trustProxy lets app.ClientIP read X-Forwarded-For (<PRODUCT>_TRUST_PROXY=on).
	trustProxy bool
	// secrets is <PRODUCT>_SECRETS: where the environment keeps its secrets.
	secrets secretsConfig
}

// AppConfigurer configures an app. Options win over the environment.
type AppConfigurer interface {
	appConfigure(o *appOptions)
}

type appOptions struct {
	env, addr, dataDir *string
	studio, analyze    *bool
	memory             bool
	logs               io.Writer
	clock              clock.Timed
	secrets            secret.Store
	// configFiles and settings are ConfigFiles' and Set's (setting.go).
	configFiles fs.FS
	settings    map[string]any
	// binds are the app's app.Bind, in the order given (port.go).
	binds []bindDecl
	// replaces are the app's app.Replace, in the order given (replace.go).
	replaces []replaceDecl
	// databases are what Database declared (database.go).
	databases []*database
	// mounts are the modules the app mounts, in the order given (mount.go);
	// withAt is where the App.With applying them was called, while it does.
	mounts []mountDecl
	withAt pos
	// analyzer is Analyzer's (analysis.go): nil, no static analysis runs.
	analyzer AnalyzeFunc
	// profile, idle and singleton are Profile's, IdleStop's and Singleton's
	// (profiles.go).
	profile   string
	idle      time.Duration
	singleton string
	// singletonPer are SingletonPer's scopes; singleton is then their label.
	singletonPer []ScopeValue
	// telemetry and telemetryGIDs are Telemetry's (telemetry.go).
	telemetry     *string
	telemetryGIDs []int
	// digest is DesignDigest's (telemetry.go).
	digest string
}

type appOption func(o *appOptions)

// memoryOption is both a StoreOption and an AppOption.
type memoryOption struct{}

// settingSources says where a setting of kit's own comes from.
type settingSources struct {
	// getenv reads the environment.
	getenv func(string) string
	// forced are the variables kit dev sets.
	forced map[string]bool
}

// clone copies the options for App.With: what an option changes in place —
// a map, a list — is copied, so an option given to the copy never reaches the
// original. The rest are values, or references an option replaces whole.
func (o appOptions) clone() appOptions {
	o.settings = maps.Clone(o.settings)
	o.binds = slices.Clone(o.binds)
	o.replaces = slices.Clone(o.replaces)
	o.databases = slices.Clone(o.databases)
	o.mounts = slices.Clone(o.mounts)
	return o
}

// appConfigure sets the option on what it configures.
func (f appOption) appConfigure(o *appOptions) { f(o) }

// Env selects the environment, instead of <PRODUCT>_ENV.
func Env(env string) AppConfigurer { return appOption(func(o *appOptions) { o.env = &env }) }

// Listen sets the listening address, instead of <PRODUCT>_ADDR or PORT. ":0" picks
// a free port; App.URL tells which.
func Listen(addr string) AppConfigurer { return appOption(func(o *appOptions) { o.addr = &addr }) }

// DataDir sets where stores and queues persist, instead of <PRODUCT>_DATA_DIR.
// Given to an app after [InMemory], it wins: the last of the two says where
// the data is — so a test helper that runs the product in memory runs it
// on disk with the one option its test gives (apptest.NewDisk).
func DataDir(dir string) AppConfigurer {
	return appOption(func(o *appOptions) { o.dataDir, o.memory = &dir, false })
}

// Studio switches the Studio on or off in dev. It is always off in
// production.
func Studio(on bool) AppConfigurer { return appOption(func(o *appOptions) { o.studio = &on }) }

// Analyze switches the in-process static analysis on or off in dev.
func Analyze(on bool) AppConfigurer { return appOption(func(o *appOptions) { o.analyze = &on }) }

// Logs sends the app's logs to w instead of standard error.
func Logs(w io.Writer) AppConfigurer { return appOption(func(o *appOptions) { o.logs = w }) }

// Clock makes the app read and wait on c instead of the system clock: a
// test drives timer transitions and jobs with a clock.ManualClock.
func Clock(c clock.Timed) AppConfigurer { return appOption(func(o *appOptions) { o.clock = c }) }

// storeConfigure sets the option on what it configures.
func (memoryOption) storeConfigure(o *storeOptions) { o.inMemory = true }

// appConfigure sets the option on what it configures.
func (memoryOption) appConfigure(o *appOptions) { o.memory = true }

// InMemory keeps data in memory. On an app, it overrides the data directory
// for every store and queue — <PRODUCT>_DATA_DIR, and a [DataDir] given before it:
// what a test wants. On a store, it keeps that store in memory even when the
// app has a data directory.
func InMemory() interface {
	StoreConfigurer
	AppConfigurer
} {
	return memoryOption{}
}

// resolveConfig merges options over the environment over the defaults. It is
// closed by default: without <PRODUCT>_ENV=dev, nothing but the product and its
// health probes is served.
func resolveConfig(o *appOptions, name string, getenv func(string) string) config {
	own := ownEnv(name, getenv)
	cfg := config{logs: o.logs, clock: o.clock, logLevel: strings.ToLower(own(varLogLevel))}
	cfg.defaultOutputs()
	env := own(varEnv)
	if o.env != nil {
		env = *o.env
	}
	cfg.env, cfg.envName = resolveEnv(env)
	dev := cfg.env == EnvDev
	cfg.addr = resolveAddr(o, own, getenv, dev)
	cfg.dataDir, cfg.memoryData = resolveDataDir(o, own, dev)
	studio := studioEnv(getenv)
	cfg.studio = resolveStudio(o, studio, dev)
	if !dev && studioAsked(o, studio) {
		cfg.warnings = append(cfg.warnings, say("config.studio-production"))
	}
	cfg.trustProxy = own(varTrustProxy) == "on"
	cfg.secrets = resolveSecrets(o, own)
	cfg.allowedHosts = slices.Clone(loopbackHosts)
	for h := range strings.SplitSeq(own(varAllowedHosts), ",") {
		if h = strings.TrimSpace(h); h != "" {
			cfg.allowedHosts = append(cfg.allowedHosts, strings.ToLower(h))
		}
	}
	return cfg
}

// ownEnv reads the framework's own variables of the product named name, by
// their stem: own(varDataDir) is TODO_DATA_DIR's value for "todo".
func ownEnv(name string, getenv func(string) string) func(stem string) string {
	prefix := appPrefix(name)
	return func(stem string) string { return getenv(prefix + "_" + stem) }
}

// resolveEnv is the environment <PRODUCT>_ENV names — dev or production —
// and the name its configuration file takes: as <PRODUCT>_ENV is written,
// config/local.yaml for local.
func resolveEnv(env string) (kind, name string) {
	switch name = strings.ToLower(strings.TrimSpace(env)); name {
	case "dev", "development", "local":
		return EnvDev, name
	case "":
		return EnvProduction, EnvProduction
	default:
		return EnvProduction, name
	}
}

// resolveAddr is where the app listens: its option, <PRODUCT>_ADDR, the
// platform's PORT — on loopback in dev, where the Studio stays —, or port
// 4000.
func resolveAddr(o *appOptions, own, getenv func(string) string, dev bool) string {
	port := getenv("PORT")
	switch {
	case o.addr != nil:
		return *o.addr
	case own(varAddr) != "":
		return own(varAddr)
	case port != "" && dev:
		return "127.0.0.1:" + port
	case port != "":
		return ":" + port
	case dev:
		return "127.0.0.1:4000"
	default:
		return ":4000"
	}
}

// resolveDataDir is where the app keeps its data — "" for memory — and
// whether it keeps it in memory for want of a directory, which is said.
func resolveDataDir(o *appOptions, own func(string) string, dev bool) (dir string, warn bool) {
	switch {
	case o.memory:
		return "", false
	case o.dataDir != nil:
		return *o.dataDir, false
	case own(varDataDir) != "":
		return own(varDataDir), false
	case dev:
		return ".kit/data", false
	default:
		return "", true
	}
}

// defaultOutputs gives the config the process's standard error for its logs
// and the system clock, when the code gives neither.
func (c *config) defaultOutputs() {
	if c.logs == nil {
		c.logs = os.Stderr
	}
	if c.clock == nil {
		c.clock = clock.System
	}
}

// studioEnv is what the tool's variables ask of the Studio, which the
// Studio subsystem reads (plug.Studio, ADR 0180 step 1): the zero value — as
// if none were set — in a program that does not import framework/app/studio,
// whose core reads none of them.
func studioEnv(getenv func(string) string) plug.StudioEnvValue {
	d := plug.Studio.Load()
	if d == nil {
		return plug.StudioEnvValue{}
	}
	return d.Env(getenv)
}

// studioAsked reports whether the code or the environment asks for the
// Studio, which production refuses.
func studioAsked(o *appOptions, env plug.StudioEnvValue) bool {
	return env.Studio == "on" || (o.studio != nil && *o.studio)
}

// resolveStudio is what the Studio may do: served in dev unless turned off,
// to remote clients when they opt in — a dev container reached through a
// forwarded port is not loopback for the process —, and with the static
// analysis unless turned off.
func resolveStudio(o *appOptions, env plug.StudioEnvValue, dev bool) studioConfig {
	on := dev && env.Studio != "off"
	if o.studio != nil {
		on = dev && *o.studio
	}
	analyze := on && !env.NoAnalysis
	if o.analyze != nil {
		analyze = on && *o.analyze
	}
	return studioConfig{on: on, remote: on && env.Remote, analyze: analyze}
}

// devForced reads which variables kit dev set for this launch, as the
// Studio subsystem reads them (plug.StudioDriverConfig.Forced): a label and
// nothing else — a name kit does not let kit dev set is dropped, and it
// never changes a value. None without framework/app/studio.
func devForced(product string, getenv func(string) string) map[string]bool {
	out := map[string]bool{}
	d := plug.Studio.Load()
	if d == nil {
		return out
	}
	for _, name := range d.Forced(getenv) {
		if slices.ContainsFunc(devForcible, func(stem string) bool { return name == productVariable(product, stem) }) {
			out[name] = true
		}
	}
	return out
}

// studioSettings are the tool's variables among the settings, as the Studio
// subsystem shows them; none without framework/app/studio.
func studioSettings(o *appOptions, getenv func(string) string, c *config) []model.Setting {
	d := plug.Studio.Load()
	if d == nil {
		return nil
	}
	return d.Settings(getenv, plug.StudioStateValue{
		StudioOption: o.studio, AnalyzeOption: o.analyze,
		Studio: onOff(c.studio.on), Remote: onOff(c.studio.remote), Analyze: onOff(c.studio.analyze),
	})
}

// settings is what the start read, in the order kit resolves it: each
// setting's value — never a secret's — where it came from — the product's
// code (an AppOption, which wins), kit dev for this launch, the environment,
// or kit's default — and the facts to change it: its AppOption and its
// kit dev flag, when it has them.
func settings(o *appOptions, name string, getenv func(string) string, c *config) []model.Setting {
	src := settingSources{getenv: getenv, forced: devForced(name, getenv)}
	dataOption := "app.DataDir"
	if o.memory {
		dataOption = "app.InMemory"
	}
	env, addr, data := productVariable(name, varEnv), productVariable(name, varAddr), productVariable(name, varDataDir)
	trust, hosts, level := productVariable(name, varTrustProxy), productVariable(name, varAllowedHosts), productVariable(name, varLogLevel)
	return slices.Concat([]model.Setting{
		{Name: env, Value: c.env, From: src.from(o.env != nil, env), Option: "app.Env"},
		{Name: addr, Value: c.addr, From: src.from(o.addr != nil, addr, "PORT"), Option: "app.Listen", Flag: "-addr"},
		{Name: data, Value: cmp.Or(c.dataDir, "memory"), From: src.from(o.memory || o.dataDir != nil, data), Option: dataOption, Flag: "-data"},
	}, studioSettings(o, getenv, c), []model.Setting{
		{Name: trust, Value: onOff(c.trustProxy), From: src.from(false, trust)},
		{Name: hosts, Value: strings.Join(c.allowedHosts, ","), From: src.from(false, hosts)},
		{Name: level, Value: cmp.Or(c.logLevel, "info"), From: src.from(false, level)},
	})
}

// from is where the setting read from names comes from: its option when the
// code gives one, the environment — or kit dev, which set it — when a name
// is set, its default otherwise.
func (s settingSources) from(option bool, names ...string) string {
	if option {
		return model.SettingOption
	}
	if !slices.ContainsFunc(names, func(n string) bool { return s.getenv(n) != "" }) {
		return model.SettingDefault
	}
	if s.forced[names[0]] {
		return model.SettingAppDev
	}
	return model.SettingEnv
}

// onOff spells a switch.
func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// secretSettings are the settings of the environment's secrets, once its
// stores are open: where it keeps them and, for a mailer, whether the SMTP
// URL is set and where, and for a database its URL's — never a value.
func (a *App) secretSettings(ctx context.Context) []model.Setting {
	out := slices.Concat(a.secretStoreSettings(ctx), a.smtpSettings(ctx), a.databaseSecretSettings(ctx))
	// Those of the personal data it keeps: <PRODUCT>_RETENTION and
	// <PRODUCT>_INDEX_KEY.
	return append(out, a.privacySettings(ctx)...)
}

// secretStoreSettings are <PRODUCT>_SECRETS, and for a file store its key:
// whether the environment gives it, or where the key file beside the data
// is.
func (a *App) secretStoreSettings(ctx context.Context) []model.Setting {
	if a.secrets.Load() == nil {
		return nil
	}
	c := a.cfg.secrets
	out := []model.Setting{{Name: a.variable(varSecrets), Value: c.describe(a.dataDir), From: c.from}}
	if c.kind != secretsFile {
		return out
	}
	key := model.Setting{Name: a.variable(varSecretsKey), Secret: true, From: model.SettingDefault}
	switch _, err := a.secretsNow().kitEnv.Get(ctx, "secrets-key"); {
	case err == nil:
		key.From = model.SettingEnv
	case a.dataDir != "" && (c.dir == "" || a.cfg.env == EnvDev):
		// The key file beside the store: where it is, not what it holds.
		key.Secret, key.Value = false, "file:"+filepath.Join(a.dataDir, besideData, "key")
	}
	return append(out, key)
}

// smtpSettings is <PRODUCT>_SMTP_URL, when a mailer reads it. A password
// lives in it: whether it is set, and where, is all that shows.
func (a *App) smtpSettings(ctx context.Context) []model.Setting {
	if !a.hasMailer() {
		return nil
	}
	_, from, err := a.kitSecret(ctx, smtpSecret)
	if err != nil {
		from = ""
	}
	return []model.Setting{{Name: a.variable(varSMTPURL), Secret: true, From: settingFrom(from)}}
}
