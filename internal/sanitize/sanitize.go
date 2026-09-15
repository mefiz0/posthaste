// Package sanitize strips active and remote content from untrusted email HTML
// before it is stored or rendered. It is the primary defense for the reading
// pane; the frontend renders the result in a sandboxed iframe with no script
// and no same-origin tokens as a second layer.
//
// The policy is an allowlist: markup not explicitly permitted is removed along
// with its attributes. Remote loads (http/https images and other remote
// references) are blocked unless allowRemote is true — the per-message "load
// remote content" opt-in re-sanitizes with it enabled.
package sanitize

import "golang.org/x/net/html"

// policy is the immutable bluemonday allowlist both sanitizing modes share. It
// is built once and only read afterwards, so it is safe for concurrent use.
// The element, attribute, and style rules live in policy.go.
var policy = buildPolicy()

// HTML returns the sanitized HTML. Remote image sources are removed unless
// allowRemote is true; data: and cid: sources always survive so locally
// available images render without any network access. Anchors are forced to
// open in a new context with noopener/noreferrer/nofollow.
func HTML(dirty string, allowRemote bool) string {
	// bluemonday applies its URL scheme allowlist globally rather than per
	// element, so data:/cid: image sources require permitting those schemes
	// everywhere; refine re-checks schemes per element on the sanitized tree
	// and drops remote images unless the caller opted in.
	cleaned := policy.Sanitize(dirty)
	return refine(cleaned, allowRemote)
}

// HasRemoteContent reports whether the HTML references remote resources, used
// to decide whether to offer the per-message load-remote opt-in. Plain links
// do not count: they are user-initiated navigation, not automatic loads. Only
// references the opt-in would actually enable (http/https image sources)
// count, so a true result is never a false promise. The scan runs on the
// parsed tree with entity decoding and URL parsing, so casing, whitespace, and
// HTML-entity tricks in the source cannot hide a reference.
func HasRemoteContent(dirty string) bool {
	body, ok := parseFragment(dirty)
	if !ok {
		return false
	}

	remote := false
	eachNode(body, func(n *html.Node) {
		if remote || n.Type != html.ElementNode || n.Data != "img" {
			return
		}
		if isRemoteURL(attrValue(n, "src")) {
			remote = true
		}
	})
	return remote
}
