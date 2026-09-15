package sanitize

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// mustNotContain lists substrings that mean active content survived. Checks
// are case-insensitive because hostile markup routinely mixes case.
var mustNotContain = []string{
	"<script",
	"javascript:",
	"vbscript:",
	"onerror=",
	"onload=",
	"<iframe",
	"<form",
	"<object",
	"<embed",
	"<input",
	"<base",
	"<meta",
	"srcdoc",
	"url(",
	"expression(",
}

// assertClean fails when out contains any never-survive substring or parses
// into a tree holding active content.
func assertClean(t *testing.T, out string) {
	t.Helper()

	lower := strings.ToLower(out)
	for _, bad := range mustNotContain {
		if strings.Contains(lower, bad) {
			t.Errorf("output contains %q:\n%s", bad, out)
		}
	}

	body, ok := parseFragment(out)
	if !ok {
		return
	}
	eachNode(body, func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		switch n.Data {
		case "script", "iframe", "form", "input", "object", "embed", "base", "meta", "link", "svg", "math", "frame", "frameset", "textarea", "select", "button", "style", "title", "head", "template":
			t.Errorf("active or unsupported element <%s> survived:\n%s", n.Data, out)
		}
		for _, a := range n.Attr {
			key := strings.ToLower(a.Key)
			if strings.HasPrefix(key, "on") {
				t.Errorf("event handler attribute %q survived on <%s>:\n%s", a.Key, n.Data, out)
			}
			switch key {
			case "href":
				if n.Data == "a" && !linkSchemeAllowed(a.Val) {
					t.Errorf("anchor href %q has a forbidden scheme:\n%s", a.Val, out)
				}
			case "src":
				if n.Data == "img" && !imgSrcAllowed(a.Val, true) {
					t.Errorf("img src %q is not permitted even with remote allowed:\n%s", a.Val, out)
				}
			case "style":
				lowerVal := strings.ToLower(a.Val)
				if strings.Contains(lowerVal, "url(") || strings.Contains(lowerVal, "expression(") {
					t.Errorf("style attribute references URLs or expressions:\n%s", out)
				}
			case "rel":
				if n.Data == "a" && attrValue(n, "href") != "" {
					want := "noopener noreferrer nofollow"
					if a.Val != want {
						t.Errorf("anchor rel = %q, want exactly %q:\n%s", a.Val, want, out)
					}
				}
			case "target":
				if n.Data == "a" && attrValue(n, "href") != "" && a.Val != "_blank" {
					t.Errorf("anchor target = %q, want _blank:\n%s", a.Val, out)
				}
			}
		}
	})
}

func TestHTMLStripsActiveContent(t *testing.T) {
	tests := []struct {
		name        string
		dirty       string
		mustContain []string
	}{
		{
			name:        "script element removed but text kept",
			dirty:       `<p>before</p><script>alert(1)</script><p>after</p>`,
			mustContain: []string{"before", "after"},
		},
		{
			name:        "mixed case script",
			dirty:       `<ScRiPt SrC="//evil.example/x.js">alert(1)</sCrIpT>text`,
			mustContain: []string{"text"},
		},
		{
			name:        "nested script tag trick",
			dirty:       `<scr<script>ipt>alert(1)</scr</script>ipt>tail`,
			mustContain: []string{"tail"},
		},
		{
			name:        "event handler stripped",
			dirty:       `<img src="cid:x@y" oNeRrOr="alert(1)" ONLOAD="alert(2)">`,
			mustContain: []string{`src="cid:x@y"`},
		},
		{
			name:        "javascript url in link",
			dirty:       `<a href="JaVaScRiPt:alert(1)">click</a>`,
			mustContain: []string{"click"},
		},
		{
			name:        "entity encoded javascript url in link",
			dirty:       `<a href="&#106;&#97;vascript:alert(1)">click</a>`,
			mustContain: []string{"click"},
		},
		{
			name:        "vbscript url in link",
			dirty:       `<a href="vbscript:msgbox(1)">click</a>`,
			mustContain: []string{"click"},
		},
		{
			name:        "data url in link",
			dirty:       `<a href="data:text/html,<b>pwned</b>">click</a>`,
			mustContain: []string{"click"},
		},
		{
			name:        "iframe removed",
			dirty:       `<iframe src="https://evil.example"></iframe>after`,
			mustContain: []string{"after"},
		},
		{
			name:        "form and input removed",
			dirty:       `<form action="https://evil.example"><input type="text" name="q"><input type="submit"></form>kept`,
			mustContain: []string{"kept"},
		},
		{
			name:        "svg event handlers removed",
			dirty:       `<svg onload="alert(1)"><circle r="1"/></svg>word`,
			mustContain: []string{"word"},
		},
		{
			name:        "meta refresh removed",
			dirty:       `<meta http-equiv="refresh" content="0;url=https://evil.example">body text`,
			mustContain: []string{"body text"},
		},
		{
			name:        "base href removed",
			dirty:       `<base href="https://evil.example/">paragraph`,
			mustContain: []string{"paragraph"},
		},
		{
			name:        "style url reference removed",
			dirty:       `<div style="background:url(https://evil.example/x.png)">visible</div>`,
			mustContain: []string{"visible"},
		},
		{
			name:        "style position trick removed",
			dirty:       `<div style="position:fixed;left:-9999px;top:0">secret text</div>`,
			mustContain: []string{"secret text"},
		},
		{
			name:        "conditional comment with script removed",
			dirty:       `<!--[if IE]><script>alert(1)</script><![endif]-->visible`,
			mustContain: []string{"visible"},
		},
		{
			name:        "object and embed removed",
			dirty:       `<object data="https://evil.example/x.swf"></object><embed src="https://evil.example/y.swf">tail`,
			mustContain: []string{"tail"},
		},
		{
			name:        "srcdoc removed with iframe",
			dirty:       `<iframe srcdoc="<script>alert(1)</script>"></iframe>rest`,
			mustContain: []string{"rest"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, allowRemote := range []bool{false, true} {
				out := HTML(tt.dirty, allowRemote)
				assertClean(t, out)
				for _, want := range tt.mustContain {
					if !strings.Contains(out, want) {
						t.Errorf("allowRemote=%v: output missing %q:\n%s", allowRemote, want, out)
					}
				}
			}
		})
	}
}

func TestHTMLRemoteImages(t *testing.T) {
	tests := []struct {
		name        string
		dirty       string
		remote      bool
		mustContain []string
	}{
		{
			name:   "remote image removed by default",
			dirty:  `<p>hi</p><img src="https://track.example/pixel.gif">`,
			remote: false,
		},
		{
			name:        "remote image kept with opt-in",
			dirty:       `<p>hi</p><img src="https://track.example/pixel.gif">`,
			remote:      true,
			mustContain: []string{`src="https://track.example/pixel.gif"`},
		},
		{
			name:   "uppercase and padded remote source removed by default",
			dirty:  `<IMG SRC=" HTTPS://Track.Example/Pixel.GIF ">`,
			remote: false,
		},
		{
			name:   "protocol relative source removed even with opt-in",
			dirty:  `before<img src="//evil.example/x.png">after`,
			remote: true,
		},
		{
			name:        "cid image survives both modes",
			dirty:       `<img src="cid:image001@example.com" alt="logo">`,
			remote:      false,
			mustContain: []string{`src="cid:image001@example.com"`, `alt="logo"`},
		},
		{
			name:        "data image survives both modes",
			dirty:       `<img src="data:image/png;base64,iVBORw0KGgo=">`,
			remote:      false,
			mustContain: []string{`src="data:image/png;base64,iVBORw0KGgo="`},
		},
		{
			name:   "relative source removed",
			dirty:  `<img src="images/logo.png">`,
			remote: true,
		},
		{
			name:        "srcset never survives",
			dirty:       `<img src="cid:x@y" srcset="https://evil.example/x.png 2x">`,
			remote:      false,
			mustContain: []string{`src="cid:x@y"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := HTML(tt.dirty, tt.remote)
			assertClean(t, out)
			for _, want := range tt.mustContain {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q:\n%s", want, out)
				}
			}
		})
	}
}

func TestHTMLForcesLinkAttributes(t *testing.T) {
	out := HTML(`<a href="https://example.com/page">read this</a>`, false)

	for _, want := range []string{
		`href="https://example.com/page"`,
		`target="_blank"`,
		`rel="noopener noreferrer nofollow"`,
		"read this",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	// A supplied weaker rel must be replaced, not preserved.
	weakened := HTML(`<a href="https://example.com" rel="opener">x</a>`, false)
	assertClean(t, weakened)
	if !strings.Contains(weakened, `rel="noopener noreferrer nofollow"`) {
		t.Errorf("supplied rel not normalized:\n%s", weakened)
	}

	// mailto keeps the href and the forced attributes.
	mailto := HTML(`<a href="mailto:hi@example.com">mail</a>`, false)
	if !strings.Contains(mailto, `href="mailto:hi@example.com"`) ||
		!strings.Contains(mailto, `target="_blank"`) {
		t.Errorf("mailto link mishandled:\n%s", mailto)
	}
}

func TestHTMLKeepsLegitimateMarkup(t *testing.T) {
	dirty := `<h1 style="text-align: center">Invoice</h1>` +
		`<p>Dear <b>customer</b>, <i>please</i> <u>review</u> <strong>the</strong> <em>invoice</em>.</p>` +
		`<table border="1" cellpadding="2" bgcolor="#ffffff">` +
		`<tr><td colspan="2" align="center" width="50%">item</td></tr>` +
		`</table><ul><li>one</li><li>two</li></ul>` +
		`<a href="https://example.com/ok">link</a>` +
		`<img src="cid:img@x" alt="photo" width="100" height="50">`

	out := HTML(dirty, false)
	assertClean(t, out)

	for _, want := range []string{
		"<h1",
		"<b>customer</b>", "<i>please</i>", "<u>review</u>",
		"<strong>the</strong>", "<em>invoice</em>",
		`<table border="1" cellpadding="2" bgcolor="#ffffff"`,
		`colspan="2"`, `align="center"`, `width="50%"`,
		"<li>one</li>", "<li>two</li>",
		`alt="photo"`, `width="100"`, `height="50"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "text-align") {
		t.Errorf("allowed style property dropped:\n%s", out)
	}
}

func TestHasRemoteContent(t *testing.T) {
	tests := []struct {
		name  string
		dirty string
		want  bool
	}{
		{name: "empty", dirty: "", want: false},
		{name: "plain text", dirty: "just text", want: false},
		{name: "https image", dirty: `<img src="https://x.example/p.gif">`, want: true},
		{name: "http image", dirty: `<img src="http://x.example/p.gif">`, want: true},
		{name: "uppercase scheme", dirty: `<IMG SRC="HTTPS://X.EXAMPLE/P.GIF">`, want: true},
		{name: "padded value", dirty: `<img src="   https://x.example/p.gif   ">`, want: true},
		{name: "entity encoded scheme", dirty: `<img src="&#104;ttps://x.example/p.gif">`, want: true},
		{name: "newline inside tag", dirty: "<img\n src=\"https://x.example/p.gif\">", want: true},
		{
			name:  "link does not count",
			dirty: `<a href="https://x.example/">text</a>`,
			want:  false,
		},
		{name: "cid image", dirty: `<img src="cid:x@y">`, want: false},
		{name: "data image", dirty: `<img src="data:image/png;base64,AA==">`, want: false},
		{name: "relative image", dirty: `<img src="local.png">`, want: false},
		{name: "protocol relative image never loads", dirty: `<img src="//x.example/p.gif">`, want: false},
		{name: "srcset never loads", dirty: `<img srcset="https://x.example/p 2x" src="cid:x@y">`, want: false},
		{name: "style url never loads", dirty: `<div style="background:url(https://x.example/p)">t</div>`, want: false},
		{name: "image inside script content is stripped anyway", dirty: `<script>var s="<img src='https://x.example/p'>";</script>`, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasRemoteContent(tt.dirty); got != tt.want {
				t.Errorf("HasRemoteContent(%q) = %v, want %v", tt.dirty, got, tt.want)
			}
		})
	}
}

func TestHTMLEmpty(t *testing.T) {
	if got := HTML("", false); got != "" {
		t.Errorf("HTML(empty) = %q, want empty", got)
	}
	if got := HTML("   \n\t ", false); got != "" {
		t.Errorf("HTML(whitespace) = %q, want empty", got)
	}
	if HasRemoteContent("") {
		t.Error("HasRemoteContent(empty) = true, want false")
	}
}
