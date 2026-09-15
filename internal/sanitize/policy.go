package sanitize

import (
	"regexp"

	"github.com/microcosm-cc/bluemonday"
)

// Attribute value patterns. bluemonday matches values with MatchString, which
// is unanchored, so every pattern here must anchor itself with ^...$ or a
// hostile value could smuggle a match past a safe prefix.
var (
	digitsPattern     = regexp.MustCompile(`^[0-9]{1,5}$`)
	dimensionsPattern = regexp.MustCompile(`^[0-9]{1,5}%?$`)
	alignPattern      = regexp.MustCompile(`^(left|center|right|justify)$`)
	valignPattern     = regexp.MustCompile(`^(top|middle|bottom|baseline)$`)
	dirPattern        = regexp.MustCompile(`^(ltr|rtl|auto)$`)
	colorPattern      = regexp.MustCompile(`^#[0-9a-f]{3,8}$|^[a-z]{1,32}$`)
	facePattern       = regexp.MustCompile(`^[a-z0-9 ,\-]{1,64}$`)
	langPattern       = regexp.MustCompile(`^[a-z]{1,8}(-[a-z0-9]{1,8})*$`)
	// freeTextPattern covers human-typed attribute values (title, summary).
	// It excludes angle brackets, quotes, and other markup-significant
	// characters so the value can never break out of its attribute.
	freeTextPattern = regexp.MustCompile(`^[\p{L}\p{N}\s\-_',:\[\]!\./\\\(\)\?&=;@#%$\*]+$`)
)

// buildPolicy assembles the bluemonday allowlist for typical email markup.
// bluemonday never allows anything by default, so this list is exhaustive: an
// element, attribute, style property, or URL scheme missing here is stripped.
func buildPolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()

	allowElements(p)
	allowAttributes(p)
	allowStyles(p)

	// Drop the content of invisible scaffolding along with the elements
	// themselves; bluemonday already skips script/style/iframe/object/...
	// by default.
	p.SkipElementsContent("head", "template")

	// URL rules. RequireParseableURLs plus a closed scheme list kills
	// javascript:/vbscript:/srcdoc-style payloads on every URL-bearing
	// attribute. Relative URLs are meaningless in email, and dropping them
	// also removes protocol-relative references. The scheme list is global
	// inside bluemonday, so it includes data: and cid: for images; refine
	// re-checks schemes per element afterwards.
	p.RequireParseableURLs(true)
	p.AllowRelativeURLs(false)
	p.AllowURLSchemes("http", "https", "mailto", "cid", "data")

	// Belt and braces: refine normalizes rel/target exactly, but these make
	// the raw policy output safe on its own too.
	p.RequireNoFollowOnLinks(true)
	p.RequireNoReferrerOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)

	return p
}

// allowElements permits the markup real email is made of: paragraphs and
// headings, lists, tables, inline formatting, images, and anchors. Everything
// else — script, style, forms, frames, embedded objects, SVG — is stripped.
func allowElements(p *bluemonday.Policy) {
	p.AllowElements(
		"a", "abbr", "acronym", "address", "article", "aside",
		"b", "bdo", "blockquote", "br",
		"caption", "center", "cite", "code", "col", "colgroup",
		"dd", "del", "dfn", "div", "dl", "dt",
		"em",
		"figcaption", "figure", "font",
		"h1", "h2", "h3", "h4", "h5", "h6", "hr",
		"i", "img", "ins",
		"kbd",
		"li",
		"mark",
		"ol",
		"p", "pre",
		"q",
		"s", "samp", "section", "small", "span", "strike", "strong", "sub", "summary", "sup",
		"table", "tbody", "td", "tfoot", "th", "thead", "tr",
		"tt",
		"u", "ul",
		"var",
		"wbr",
	)
}

// allowAttributes permits a conservative presentational set. Structured
// values are matched against anchored patterns so a hostile value cannot ride
// in on an allowed attribute name.
func allowAttributes(p *bluemonday.Policy) {
	// Global: direction, language, and human-readable titles.
	p.AllowAttrs("dir").Matching(dirPattern).Globally()
	p.AllowAttrs("lang").Matching(langPattern).Globally()
	p.AllowAttrs("title").Matching(freeTextPattern).Globally()

	// Alignment.
	p.AllowAttrs("align").Matching(alignPattern).OnElements(
		"p", "div", "caption", "table", "td", "th", "tr",
		"h1", "h2", "h3", "h4", "h5", "h6", "img", "font", "hr",
	)
	p.AllowAttrs("valign").Matching(valignPattern).OnElements(
		"table", "td", "th", "tr", "thead", "tbody", "tfoot",
	)

	// Images: source is scheme-checked, alt is free text, dimensions are
	// strictly numeric. srcset is deliberately absent — it bypasses the src
	// scheme checks with a second URL.
	p.AllowAttrs("src").OnElements("img")
	p.AllowAttrs("alt").Matching(freeTextPattern).OnElements("img")
	p.AllowAttrs("width", "height").Matching(dimensionsPattern).OnElements("img", "table", "td", "th", "col", "hr")
	p.AllowAttrs("align").Matching(alignPattern).OnElements("img")

	// Anchors: href only; scheme membership is enforced by the URL policy
	// above plus refine. target/rel are added back by the policy flags and
	// normalized by refine.
	p.AllowAttrs("href").OnElements("a")

	// Tables: layout attributes old email markup relies on.
	p.AllowAttrs("border", "cellpadding", "cellspacing").Matching(digitsPattern).OnElements("table")
	p.AllowAttrs("colspan", "rowspan").Matching(digitsPattern).OnElements("td", "th")
	p.AllowAttrs("summary").Matching(freeTextPattern).OnElements("table")

	// Legacy font element.
	p.AllowAttrs("color").Matching(colorPattern).OnElements("font")
	p.AllowAttrs("face").Matching(facePattern).OnElements("font")
	p.AllowAttrs("size").Matching(digitsPattern).OnElements("font")

	// Background color on table cells and rows; a URL form (background=)
	// is deliberately not allowed.
	p.AllowAttrs("bgcolor").Matching(colorPattern).OnElements("table", "tr", "td", "th")
}

// allowStyles permits a minimal set of inline style properties. Arbitrary CSS
// is far too broad to prove safe (url(), expression(), positioning tricks,
// exfiltration via background images), so the allowlist keeps only the
// formatting email actually needs: colors, alignment, and basic font traits.
// Every value pattern is anchored and accepts no parentheses or separators,
// which structurally excludes url() and friends. Correctness is chosen over
// fidelity on purpose: a slightly plainer email beats a sanitizer bypass.
func allowStyles(p *bluemonday.Policy) {
	p.AllowStyles("color").Matching(colorPattern).Globally()
	p.AllowStyles("background-color").Matching(colorPattern).Globally()
	p.AllowStyles("text-align").Matching(alignPattern).Globally()
	p.AllowStyles("font-weight").Matching(regexp.MustCompile(`^(normal|bold|[1-9]00)$`)).Globally()
	p.AllowStyles("font-style").Matching(regexp.MustCompile(`^(normal|italic|oblique)$`)).Globally()
	p.AllowStyles("text-decoration").Matching(regexp.MustCompile(`^(none|underline|overline|line-through)$`)).Globally()
}
