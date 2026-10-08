package views

import (
	jsonv2 "encoding/json/v2"
	"strings"

	"github.com/birdple/falco/internal/jsonx"
)

// jsCall renders an Alpine expression that calls fn with args.
//
// Every argument is encoded as a JSON literal, which is also a valid JavaScript
// literal, so a bucket, prefix or key can never close the string and append
// code of its own. Building these expressions with fmt.Sprintf("f('%s')") is an
// XSS: templ HTML-escapes the attribute, but the browser decodes it back before
// Alpine evaluates it.
func jsCall(fn string, args ...any) string {
	var b strings.Builder
	b.WriteString(fn)
	b.WriteByte('(')
	for i, a := range args {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(jsLiteral(a))
	}
	b.WriteByte(')')
	return b.String()
}

// jsLiteral encodes v as a JavaScript literal. jsonx.Wire escapes <, >, & and
// U+2028/U+2029, so the result is safe inside a script context too.
func jsLiteral(v any) string {
	out, err := jsonv2.Marshal(v, jsonx.Wire)
	if err != nil {
		return "null"
	}
	return string(out)
}
