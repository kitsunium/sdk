package studioengine

import "github.com/kitsunium/sdk/framework/internal/app/plug"

// driver is what Enable plugs into kit: the tool's variables and the
// Studio's routes.
var driver = plug.StudioDriverConfig{
	Env:      studioEnv,
	Settings: studioSettings,
	Forced:   devForced,
	DevBuild: devBuildOf,
	Mount:    mount,
}

// Enable plugs the Studio into kit: the tool's variables, the Studio's
// routes — with its event stream and its profiler — and kit's catalogues
// after English.
func Enable() {
	plug.Studio.Store(&driver)
	plug.Words.Store(&words)
}
