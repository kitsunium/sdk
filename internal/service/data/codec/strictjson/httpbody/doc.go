// Package httpbody decodes the JSON body of an HTTP request with the strict
// decoder of its parent package, adding the three things only an HTTP body
// has: a bound net/http enforces too, an empty body settled before anything
// else, and a media type that must declare JSON.
//
// It is a package of its own so that the decoder alone links no net/http: a
// program that reads JSON documents from files, queues or sockets imports
// strictjson and nothing of HTTP, and only a server that reads request bodies
// imports this package and the net/http it needs anyway.
package httpbody
