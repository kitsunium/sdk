package i18n

// exitConfig matches sysexits EX_CONFIG (78). Every sentinel carrying it is a
// permanent wiring fault raised while a catalogue or a value is being BUILT:
// the same input will be refused forever, and the fix is a catalogue or code
// change, never a retry.
const exitConfig int = 78
