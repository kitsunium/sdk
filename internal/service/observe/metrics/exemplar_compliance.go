package metrics

// The instruments that keep exemplars take the meter's clock at admission
// (#869).
var (
	_ setClocker = (*exemplarSlots)(nil)
	_ setClocker = (*memSum)(nil)
	_ setClocker = (*memHistogram)(nil)
	_ setClocker = (*memExpHistogram)(nil)
)
