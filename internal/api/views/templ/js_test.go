package views

import (
	jsonv2 "encoding/json/v2"
	"strings"
	"testing"
)

func TestJSCall_ArgumentsCannotBreakOut(t *testing.T) {
	hostile := []string{
		"local",
		"x', alert(1), '",
		`x", alert(1), "`,
		"</div><script>alert(1)</script>",
		"a" + string(rune(0x2028)) + "b" + string(rune(0x2029)),
		`back\slash`,
	}
	for _, in := range hostile {
		got := jsCall("f", in)
		if !strings.HasPrefix(got, `f("`) || !strings.HasSuffix(got, `")`) {
			t.Fatalf("jsCall(%q) = %s: not a single string argument", in, got)
		}
		if strings.ContainsAny(got, "<>"+string(rune(0x2028))+string(rune(0x2029))) {
			t.Fatalf("jsCall(%q) = %s: unescaped HTML/JS-significant character", in, got)
		}
		var back string
		if err := jsonv2.Unmarshal([]byte(got[2:len(got)-1]), &back); err != nil || back != in {
			t.Fatalf("jsCall(%q) = %s: does not round-trip (%v, %q)", in, got, err, back)
		}
	}
}

func TestJSCall_NonStringArguments(t *testing.T) {
	if got := jsCall("signer", 3600, true); got != "signer(3600, true)" {
		t.Fatalf("got %s", got)
	}
}
