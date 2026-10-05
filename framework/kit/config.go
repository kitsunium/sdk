package kit

import (
	"io"

	ikit "github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

const (
	// EnvDev serves the Studio, analyzes the source and binds loopback. Only
	// `kit dev` or an explicit KIT_ENV=dev turns it on.
	EnvDev string = ikit.EnvDev

	// EnvProduction is the default: no Studio, no source, no analysis.
	EnvProduction string = ikit.EnvProduction
)

// AppOption configures an app. Options win over the environment.
type AppOption = ikit.AppConfigurer

// Env selects the environment, instead of KIT_ENV.
func Env(env string) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Env(env)
}

// Listen sets the listening address, instead of KIT_ADDR or PORT. ":0" picks
// a free port; App.URL tells which.
func Listen(addr string) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Listen(addr)
}

// DataDir sets where stores and queues persist, instead of KIT_DATA_DIR.
func DataDir(dir string) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.DataDir(dir)
}

// Studio switches the Studio on or off in dev. It is always off in
// production.
func Studio(on bool) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Studio(on)
}

// Analyze switches the in-process static analysis on or off in dev.
func Analyze(on bool) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Analyze(on)
}

// Logs sends the app's logs to w instead of standard error.
func Logs(w io.Writer) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Logs(w)
}

// Clock makes the app read and wait on c instead of the system clock: a
// test drives timer transitions and jobs with a clock.ManualClock.
func Clock(c clock.Timed) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Clock(c)
}

// InMemory keeps data in memory. On an app, it overrides the data directory
// for every store and queue — what a test wants. On a store, it keeps that
// store in memory even when the app has a data directory.
func InMemory() interface {
	StoreOption
	AppOption
} {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.InMemory()
}
