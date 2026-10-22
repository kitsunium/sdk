package app

// SupervisionConfigurer configures what the framework restarts after a
// failure: a subscription's, a watch's or a queued command's consumer, a
// declared loop, a hand-written loop ([GiveUpAfter]).
type SupervisionConfigurer interface {
	DeliveryOption
	LoopConfigurer
	RoutineConfigurer
}

// RoutineConfigurer configures a hand-written loop ([Service.Go]).
type RoutineConfigurer interface {
	routineConfigure(o *routineOptions)
}
