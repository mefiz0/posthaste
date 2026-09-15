package sanitize

import (
	"net/url"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// parseFragment parses an HTML fragment in a body context and returns the
// context node with the fragment attached as children. ParseFragment hands
// back detached nodes, so they are re-linked here; that makes later removals
// visible when rendering. The parser is designed for hostile input and never
// fails hard: ok is false only for empty or unparseable input.
func parseFragment(dirty string) (*html.Node, bool) {
	if strings.TrimSpace(dirty) == "" {
		return nil, false
	}
	body := &html.Node{
		Type:     html.ElementNode,
		Data:     "body",
		DataAtom: atom.Body,
	}
	nodes, err := html.ParseFragment(strings.NewReader(dirty), body)
	if err != nil || len(nodes) == 0 {
		// ParseFragment does not fail on any realistic byte sequence; treat
		// an error as "nothing safe to say about this input".
		return nil, false
	}
	for _, n := range nodes {
		body.AppendChild(n)
	}
	return body, true
}

// eachNode calls fn for every node in the fragment, depth first.
func eachNode(body *html.Node, fn func(n *html.Node)) {
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		fn(n)
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	for child := body.FirstChild; child != nil; child = child.NextSibling {
		walk(child)
	}
}

// refine re-parses already-sanitized HTML and enforces the two rules
// bluemonday cannot express per element:
//
//   - remote image sources load only when the user opted in;
//   - anchor hrefs carry only web and mailto schemes, because the global URL
//     scheme allowlist that makes data:/cid: images work would otherwise let
//     a data: or cid: href through as well.
//
// It also normalizes link target/rel attributes, which the policy flags only
// add to fully qualified links. The input has already been through the
// bluemonday allowlist, so this pass never encounters active content; it is a
// second, stricter pass rather than a substitute.
func refine(sanitized string, allowRemote bool) string {
	body, ok := parseFragment(sanitized)
	if !ok {
		return ""
	}

	// Removals detach nodes and clear their sibling links, so every loop
	// captures the next node before processing the current one.
	for child := body.FirstChild; child != nil; {
		next := child.NextSibling
		refineNode(child, allowRemote)
		child = next
	}

	var b strings.Builder
	for child := body.FirstChild; child != nil; child = child.NextSibling {
		// Rendering in-memory nodes only fails on writer errors, which a
		// strings.Builder cannot produce; fall back to the sanitized input
		// should that ever change.
		if err := html.Render(&b, child); err != nil {
			return sanitized
		}
	}
	return b.String()
}

// refineNode applies the per-element rules, removing nodes from the tree as
// needed. The next sibling is captured before recursing so removals cannot
// break the iteration.
func refineNode(n *html.Node, allowRemote bool) {
	if n.Type == html.ElementNode {
		switch n.Data {
		case "img":
			if !imgSrcAllowed(attrValue(n, "src"), allowRemote) {
				removeNode(n)
				return
			}
		case "a":
			if href := attrValue(n, "href"); href != "" {
				if !linkSchemeAllowed(href) {
					removeAttr(n, "href")
					removeAttr(n, "target")
					removeAttr(n, "rel")
				} else {
					// Overwrite rather than append so a supplied target or
					// rel cannot survive with weaker values.
					setAttr(n, "target", "_blank")
					setAttr(n, "rel", "noopener noreferrer nofollow")
				}
			}
		}
	}

	child := n.FirstChild
	for child != nil {
		next := child.NextSibling
		refineNode(child, allowRemote)
		child = next
	}
}

// imgSrcAllowed enforces the image source policy: data: and cid: sources
// always load (cid: values resolve locally against the attachment store),
// http/https only after the explicit opt-in. Relative and protocol-relative
// sources are gone already — the policy drops every relative URL — and are
// refused here too in case that ever changes.
func imgSrcAllowed(src string, allowRemote bool) bool {
	scheme := urlScheme(src)
	switch scheme {
	case "data", "cid":
		return true
	case "http", "https":
		return allowRemote
	default:
		return false
	}
}

// linkSchemeAllowed keeps only schemes the desktop can open safely in an
// external browser or mail reader.
func linkSchemeAllowed(href string) bool {
	switch urlScheme(href) {
	case "http", "https", "mailto":
		return true
	default:
		return false
	}
}

// isRemoteURL reports whether the value points at a remote http(s) resource.
func isRemoteURL(value string) bool {
	switch urlScheme(value) {
	case "http", "https":
		return true
	default:
		return false
	}
}

// urlScheme extracts the scheme of an attribute value. Attribute values are
// entity-decoded by the parser, so encoded tricks like &#106;avascript: are
// already resolved before this runs; a value that fails to parse has no
// trusted scheme.
func urlScheme(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return parsed.Scheme
}

// attrValue returns the value of the named attribute, or empty.
func attrValue(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// setAttr sets the named attribute, replacing any existing value.
func setAttr(n *html.Node, key, val string) {
	for i, a := range n.Attr {
		if a.Key == key {
			n.Attr[i].Val = val
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: val})
}

// removeAttr deletes the named attribute if present.
func removeAttr(n *html.Node, key string) {
	for i, a := range n.Attr {
		if a.Key == key {
			n.Attr = append(n.Attr[:i], n.Attr[i+1:]...)
			return
		}
	}
}

// removeNode detaches n from its parent.
func removeNode(n *html.Node) {
	if n.Parent == nil {
		return
	}
	n.Parent.RemoveChild(n)
}
