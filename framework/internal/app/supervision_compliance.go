package app

// : Asserts at compile time that GiveUpAfter's option configures every
// supervised component — a subscription's, a watch's and a queued command's
// consumer, a declared and a hand-written loop.
var (
	_ SupervisionConfigurer  = (*giveUpOption)(nil)
	_ DeliveryOption         = (*giveUpOption)(nil)
	_ SubscriptionConfigurer = (*giveUpOption)(nil)
	_ CommandConfigurer      = (*giveUpOption)(nil)
	_ LoopConfigurer         = (*giveUpOption)(nil)
	_ RoutineConfigurer      = (*giveUpOption)(nil)
)
