package app

// JobConfigurer configures a job ([Service.Every], [Service.Cron]):
// [Overlap], [StartingDeadline].
type JobConfigurer interface {
	jobConfigure(k *jobKnobs)
}
