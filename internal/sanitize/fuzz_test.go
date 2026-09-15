package sanitize

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// fuzzSeeds are the adversarial inputs the sanitizer must already survive;
// they give the fuzz engine a head start toward interesting structures.
func fuzzSeeds() []string {
	return []string{
		`<p>normal paragraph</p>`,
		`<script>alert(1)</script>after`,
		`<ScRiPt SrC=//evil.example/x.js></ScRiPt>t`,
		`<scr<script>ipt>alert(1)</script>`,
		`<img src=x onerror=alert(1)>`,
		`<img src="JaVaScRiPt:alert(1)">`,
		`<a href="&#106;avascript:alert(1)">c</a>`,
		`<a href="&#x6A;avascript&#58;alert(1)">c</a>`,
		`<a href="data:text/html,<script>alert(1)</script>">d</a>`,
		`<iframe srcdoc="<script>alert(1)</script>"></iframe>`,
		`<form action="https://evil.example"><input type=submit></form>`,
		`<svg onload=alert(1)><circle r=1></svg>`,
		`<div style="background:url(javascript:alert(1))">t</div>`,
		`<div style="color:expression(alert(1))">t</div>`,
		`<table><tr><td colspan=2 align=center width=100%>cell</table>`,
		`<img src="cid:x@y"><img src="data:image/png;base64,AA==">`,
		`<img src="https://track.example/p.gif"><img src="//rel.example/x">`,
		`<!-- <script>alert(1)</script> --><b>bold</b>`,
		`<![CDATA[<script>alert(1)</script>]]>tail`,
		`<math><mtext></mtext></math><base href="https://evil.example/">`,
		"<img src=\"\t\njavascript:alert(1)\">",
		"\x00\xff\xfe garbage bytes <b>bold</b>",
	}
}

// FuzzSanitize asserts the never-survive invariants over arbitrary input, in
// both remote-content modes: the sanitized output must never parse into a
// tree holding active elements, event-handler attributes, forbidden URL
// schemes, or URL-bearing styles. Run continuously with:
//
//	go test -run '^$' -fuzz FuzzSanitize ./internal/sanitize/
func FuzzSanitize(f *testing.F) {
	for _, seed := range fuzzSeeds() {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		for _, allowRemote := range []bool{false, true} {
			out := HTML(input, allowRemote)
			assertNoActiveContent(t, out, allowRemote)

			// The remote-content scan must never promise less than the
			// sanitizer would load: nothing remote may survive when the
			// opt-in is off and the scan says there is nothing remote.
			if !allowRemote && !HasRemoteContent(input) {
				assertNoRemoteImages(t, out)
			}
		}
	})
}

// assertNoActiveContent checks the structural invariants on sanitized output.
func assertNoActiveContent(t *testing.T, out string, allowRemote bool) {
	t.Helper()

	body, ok := parseFragment(out)
	if !ok {
		return
	}

	banned := map[string]struct{}{
		"script": {}, "iframe": {}, "form": {}, "input": {}, "object": {},
		"embed": {}, "base": {}, "meta": {}, "link": {}, "svg": {},
		"math": {}, "frame": {}, "frameset": {}, "style": {}, "title": {},
		"head": {}, "template": {}, "textarea": {}, "select": {}, "button": {},
	}

	eachNode(body, func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		if _, bad := banned[n.Data]; bad {
			t.Fatalf("allowRemote=%v: element <%s> survived input:\n%s", allowRemote, n.Data, out)
		}
		for _, a := range n.Attr {
			key := strings.ToLower(a.Key)
			if strings.HasPrefix(key, "on") {
				t.Fatalf("allowRemote=%v: attribute %q survived:\n%s", allowRemote, a.Key, out)
			}
			if key == "href" && n.Data == "a" && !linkSchemeAllowed(a.Val) {
				t.Fatalf("allowRemote=%v: href scheme %q survived:\n%s", allowRemote, a.Val, out)
			}
			if key == "src" && n.Data == "img" && !imgSrcAllowed(a.Val, allowRemote) {
				t.Fatalf("allowRemote=%v: img src %q survived:\n%s", allowRemote, a.Val, out)
			}
			if key == "style" {
				lowerVal := strings.ToLower(a.Val)
				if strings.Contains(lowerVal, "url(") || strings.Contains(lowerVal, "expression(") {
					t.Fatalf("allowRemote=%v: style with url/expression survived:\n%s", allowRemote, out)
				}
			}
		}
	})
}

// assertNoRemoteImages checks that no image in the output points at the
// network.
func assertNoRemoteImages(t *testing.T, out string) {
	t.Helper()

	body, ok := parseFragment(out)
	if !ok {
		return
	}
	eachNode(body, func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "img" && isRemoteURL(attrValue(n, "src")) {
			t.Fatalf("remote image survived without the opt-in:\n%s", out)
		}
	})
}
