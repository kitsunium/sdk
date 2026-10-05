package token

// verifying projects the PASETO config onto the shared verification policy, so
// the temporal and identity rules are written once for both formats.
func (c PasetoVerifierConfig) verifying() VerifierConfig {
	//: RequireType and MaxKeyCandidates stay zero: PASETO has no header to
	//: type and no key set to select from.
	return VerifierConfig{
		Issuer:             c.Issuer,
		Audience:           c.Audience,
		Leeway:             c.Leeway,
		MaxLifetime:        c.MaxLifetime,
		MaxTokenLen:        c.MaxTokenLen,
		MaxClaimDepth:      c.MaxClaimDepth,
		AllowMissingExpiry: c.AllowMissingExpiry,
		Clock:              c.Clock,
	}
}
