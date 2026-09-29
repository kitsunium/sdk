// Package kit — the interfaces of settings: the values a setting holds, and
// what kit reads of a declaration.
package kit

import (
	"time"
)

// SettingValue is what a setting may hold.
type SettingValue interface {
	string | bool | int | int64 | float64 | time.Duration | []string
}

// SettingConfigurer configures a setting.
type SettingConfigurer interface {
	settingConfigure(o *settingBase)
}

// settingDecl is a declared setting, whatever it holds.
type settingDecl interface {
	base() *settingBase
	// kind names what it holds, for a message and the Studio.
	kind() string
	defaultValue() settingValue
	// fromText reads a variable's text, or kit.Set's; fromFile a value a
	// configuration file decoded to; fromCode kit.Set's value.
	fromText(text string) (settingValue, error)
	fromFile(v fileValue) (settingValue, error)
	fromCode(v any) (settingValue, error)
	// text is a value as the configuration shows it.
	text(v settingValue) string
}
