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

// listen is Listen's body: decl_gen.go writes Listen, from the
// design, as one call of it.
func listen(addr string) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Listen(addr)
}

// dataDir is DataDir's body: decl_gen.go writes DataDir, from the
// design, as one call of it.
func dataDir(dir string) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.DataDir(dir)
}

// studio is Studio's body: decl_gen.go writes Studio, from the
// design, as one call of it.
func studio(on bool) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Studio(on)
}

// analyze is Analyze's body: decl_gen.go writes Analyze, from the
// design, as one call of it.
func analyze(on bool) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Analyze(on)
}

// logs is Logs's body: decl_gen.go writes Logs, from the
// design, as one call of it.
func logs(w io.Writer) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Logs(w)
}

// Clock makes the app read and wait on c instead of the system clock: a
// test drives timer transitions and jobs with a clock.ManualClock.
func Clock(c clock.Timed) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Clock(c)
}

// inMemory is InMemory's body: decl_gen.go writes InMemory, from the
// design, as one call of it.
func inMemory() interface {
	StoreOption
	AppOption
} {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.InMemory()
}
