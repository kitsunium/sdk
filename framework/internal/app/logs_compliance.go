package app

// redactingPrefixEncoder keeps the SDK's PrefixEncoder sibling over an
// encoder that has it (kitsunium/sdk#1016).
var _ prefixEncoder = redactingPrefixEncoder{}
