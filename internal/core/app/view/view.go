package view

// ContentTypeHTML is the value an HTML [Renderer] reports from ContentType.
//
// The charset is not decoration. A response whose Content-Type carries no
// charset is sniffed by the browser, and a document sniffed as UTF-7 or as a
// legacy multi-byte encoding can smuggle markup past an escaper that judged
// the bytes as UTF-8.
const ContentTypeHTML string = "text/html; charset=utf-8"
