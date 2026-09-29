// Package kit — the interfaces of settings: the values a setting holds, and
// what kit reads of a declaration.
package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// SettingValue is what a setting may hold.
type SettingValue = ikit.SettingValue

// SettingOption configures a setting.
type SettingOption = ikit.SettingConfigurer
