package token

// issuing projects the PASETO config onto the shared issuing policy, so the
// claim-stamping rules are written once and cannot drift between the formats.
func (c PasetoIssuerConfig) issuing() IssuerConfig {
	//: Type and KeyID stay zero: PASETO carries no header to put them in.
	return IssuerConfig{
		Issuer:             c.Issuer,
		Lifetime:           c.Lifetime,
		AllowMissingExpiry: c.AllowMissingExpiry,
		Clock:              c.Clock,
	}
}
