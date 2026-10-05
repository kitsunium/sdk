package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// Binary is one executable the product ships, made of process roles: a
// status line and its daemon are two roles of one binary, "statusline" and
// "statusline daemon". Each role is an App — its own services, its own
// profile — and two roles talk only through a declared, versioned contract
// (D22): a listener one serves and the other dials, never each other's code.
//
//	var Binary = kit.NewBinary("statusline", "The status line of a Claude session.").
//		Role("render", RenderApp).                // statusline ...
//		Role("daemon", DaemonApp, "daemon").      // statusline daemon ...
//		Talks("render", "daemon", "render/v1")
//
//	func main() { os.Exit(Binary.Main(context.Background(), os.Args[1:])) }
//
// The package that declares a binary and its roles is wiring, generated from
// the design (D22).
type Binary = ikit.Binary

// NewBinary declares the binary named name.
//
//go:noinline
func NewBinary(name, doc string) *Binary {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.NewBinary(name, doc)
}
