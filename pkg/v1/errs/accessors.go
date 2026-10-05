package errs

// HasAnyCode walks err's chain and reports whether any *errs.Error
// carries a Code matching ANY of the supplied codes. Convenience
// equivalent of `HasCode(err, c1) || HasCode(err, c2) || …` — useful
// when a retry policy / circuit-breaker / metric routes on a SET of
// failure modes rather than a single code.
//
// Returns false when codes is empty.
//
//	retryable := errs.HasAnyCode(err,
//	    codec.CodeStreamingUnsupported,
//	    logger.CodeWriterRequired,
//	    logger.CodeSinkConfigRequired,
//	)
func HasAnyCode(err error, codes ...Code) bool {
	//: short-circuit on the empty list — no candidates can match.
	for _, c := range codes {
		//: each HasCode walk is O(chain depth); typical chains are 1-3 deep.
		if HasCode(err, c) {
			//: first hit wins; no need to walk the remaining candidates.
			return true
		}
	}
	//: walked every code without a hit.
	return false
}

// HasAnyReason is the reason-string sibling of [HasAnyCode]. Reports
// whether any *errs.Error in err's chain carries a Reason matching ANY
// of the supplied reasons. Useful when call sites read more naturally
// with the SCREAMING_SNAKE label than the numeric code.
//
// Returns false when reasons is empty.
//
//	if errs.HasAnyReason(err, "UNKNOWN_FORMAT", "STREAMING_UNSUPPORTED") {
//	    // fall back to a different transport
//	}
func HasAnyReason(err error, reasons ...string) bool {
	//: same loop shape as HasAnyCode — kept intentionally parallel.
	for _, r := range reasons {
		//: per-reason walk delegated to HasReason which already handles Unwrap chains.
		if HasReason(err, r) {
			//: first match short-circuits.
			return true
		}
	}
	//: no reason in the list matched the chain.
	return false
}
