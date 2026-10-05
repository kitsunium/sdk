// Package commonpw holds the ten thousand most common passwords, and says
// whether a password is one of them (ADR 0143 §D10). Public facade:
// pkg/v1/crypto/password, IsCommon.
//
// # What the list is, and where it comes from
//
// The list is xato-net-10-million-passwords-10000.txt from SecLists
// (https://github.com/danielmiessler/SecLists, Passwords/Common-Credentials),
// embedded byte for byte as it stood at commit
// 2e3e92569043d24297ca6c35070078e5cf41651e, SHA-256
// c63d5e4ccc31344d662583cc39ca4bd5bd20517ff1d24501f0c4e0c22d9b722a: the
// 10 000 passwords that occur most often in the ten million credentials Mark
// Burnett released into the public domain in 2015, most frequent first.
//
// SecLists is distributed under the MIT licence, Copyright (c) 2018 Daniel
// Miessler; its text is LICENSE.SecLists, beside the list, and travels with it
// in the module. The SDK's own licence is MIT too.
//
// # Why this list
//
// NIST SP 800-63B-4 requires a verifier to compare a new password against a
// blocklist of commonly used ones, and OWASP ASVS 5.0 (6.2.4) asks for at least
// the top 3 000. Ten thousand covers that with room, at 76 KB in the binary.
// Its provenance is known end to end, which a list merged from breaches of
// unknown origin does not offer.
//
// # How a password is compared
//
// Case-insensitively: a password and every entry are compared lower-cased as
// strings.ToLower lower-cases them, so PASSWORD and Password are refused with
// password — a guessing attacker tries case variants first, and refusing them
// costs a person nothing. Nothing else is normalised: no trimming, no
// substitution of look-alike characters. The list is sorted once, at the first
// question, and each question is a binary search that lower-cases the password
// as it compares it: no copy of the password is made, as a string or
// otherwise, and it is neither kept nor logged.
//
// One line of the file is empty: the empty password is among the ten thousand
// most used. It is not an entry here — refusing it is a length rule's job, and
// IsCommon answers false for it — so the list holds 9 999 passwords, 9 916
// once the case variants of one another are one.
//
// A policy that requires fifteen characters, NIST's minimum for a password
// used alone, has little left to refuse here — two entries are that long — so
// the list matters most beside a shorter minimum, such as the eight NIST allows
// with a second factor.
package commonpw
