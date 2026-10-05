package kit

import (
	"io/fs"

	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// Setting is a value the environment gives a service, declared with
// [Service].Setting.
type Setting[T SettingValue] = ikit.SettingService[T]

// required is Required's body: decl_gen.go writes Required, from the
// design, as one call of it.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func required() SettingOption {
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

// configFiles is ConfigFiles's body: decl_gen.go writes ConfigFiles, from the
// design, as one call of it.
func configFiles(fsys fs.FS) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.ConfigFiles(fsys)
}
