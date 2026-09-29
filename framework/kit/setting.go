// Package kit — settings: a value the environment gives a service.
package kit

import (
	"io/fs"

	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// Setting is a value the environment gives a service, declared with
// [Service].Setting.
type Setting[T SettingValue] = ikit.SettingService[T]

// Required drops a setting's default: a start without a value for it fails,
// saying where to give one.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Required() SettingOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Required()
}

// Set gives the setting name a value in code, over the environment and the
// files: a value of the setting's type, or its text as a variable would
// hold it. It is for tests.
func Set[V any](name string, value V) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Set(name, value)
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
func ConfigFiles(fsys fs.FS) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.ConfigFiles(fsys)
}
