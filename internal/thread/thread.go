// Package thread implements JWZ-style conversation grouping over message
// metadata. It is pure and deterministic: the sync engine feeds it message
// headers at ingest time and applies the reported thread assignments and
// merges to the store; nothing here touches a database or the network.
//
// The primary grouping signal is the References/In-Reply-To ancestor chain. A
// message whose ancestors are not known yet still gets a thread keyed by the
// chain root, so when the ancestor arrives later the conversation re-unites
// and threads that had grown apart are reported as merged. Messages without a
// usable chain fall back to normalized-subject grouping within a rolling date
// window.
package thread

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DefaultWindow is the subject-fallback date window used when NewIndex is
// given a non-positive window.
const DefaultWindow = 30 * 24 * time.Hour

// Internal key prefixes. Keys anchored to a Message-ID stay derivable across
// restarts; subject-fallback threads get opaque sequence keys because the
// same subject may legitimately host several separate conversations.
const (
	midPrefix = "mid:"
	rowPrefix = "row:"
)

// Meta is the message metadata the grouping needs, as parsed from RFC 5322
// headers before ingest.
type Meta struct {
	// RowID is the local database row id of the message. It addresses nodes
	// for messages whose Message-ID header is missing or empty.
	RowID int64
	// MessageID is the RFC 5322 Message-ID. It may be empty for broken mail;
	// such messages are reachable only through their own chain or subject.
	MessageID string
	// InReplyTo is the direct parent's Message-ID, when the header is set.
	InReplyTo string
	// References lists ancestor Message-IDs, conversation root first, as the
	// header carries them.
	References []string
	// Subject is the raw subject header value.
	Subject string
	// Date is the message date; zero when unknown.
	Date time.Time
}

// Result tells the caller what to persist after ingesting one message.
type Result struct {
	// ThreadID is the thread the message now belongs to.
	ThreadID int64
	// MergedInto lists existing thread IDs whose messages must be re-pointed
	// into ThreadID. It is empty unless this ingest joined previously separate
	// conversations; once the caller applies the merges, the listed threads
	// no longer exist.
	MergedInto []int64
}

// threadState is one conversation in the graph. Members are known through
// nodeKeys; aliases records every Message-ID key that resolves here, so a
// conversation anchored by several chain roots stays findable under all of
// them after a merge.
type threadState struct {
	key      string
	dbID     int64
	subject  string
	earliest time.Time
	latest   time.Time
	nodeKeys []string
	aliases  []string
}

// Index is an in-memory conversation graph for one account. Hydrate every
// persisted message through Seed, then feed each new message to Ingest. An
// Index is not safe for concurrent use; each account's sync worker owns its
// own.
type Index struct {
	window    time.Duration
	nodes     map[string]string       // node key -> thread key
	threads   map[string]*threadState // thread key -> conversation, aliases included
	bySubject map[string][]string     // normalized subject -> thread keys, creation order
	seq       int
}

// NewIndex returns an Index using window as the subject-fallback window: a
// message without a reference chain joins a same-subject thread only when its
// date falls within window of that thread's members. A non-positive window
// selects DefaultWindow.
func NewIndex(window time.Duration) *Index {
	if window <= 0 {
		window = DefaultWindow
	}
	return &Index{
		window:    window,
		nodes:     make(map[string]string),
		threads:   make(map[string]*threadState),
		bySubject: make(map[string][]string),
	}
}

// Seed registers a message that is already persisted under the given thread
// id so a restarted process continues grouping without recomputing history.
// Call it for every persisted message before ingesting new mail. Seed
// reconstructs membership and never invents a new database identity: when the
// index already knows a thread for the message's chain root or Message-ID,
// that thread wins over the passed id.
func (ix *Index) Seed(m Meta, threadID int64) {
	nodeKey := nodeKeyFor(m)
	if _, known := ix.nodes[nodeKey]; known {
		return
	}
	chain := m.ancestorChain()
	id := normalizeMessageID(m.MessageID)

	key := ""
	switch {
	case len(chain) > 0:
		key = ix.resolveRootKey(chain)
	case id != "":
		key = midPrefix + id
	}
	if t, ok := ix.threads[key]; ok {
		ix.attach(t, nodeKey, m.Date)
		return
	}
	if t := ix.threadByDBID(threadID); t != nil {
		ix.attach(t, nodeKey, m.Date)
		return
	}
	if key == "" {
		ix.seq++
		key = fmt.Sprintf("s%d", ix.seq)
	}
	t := &threadState{
		key:     key,
		dbID:    threadID,
		subject: NormalizeSubject(m.Subject),
		aliases: []string{key},
	}
	ix.threads[key] = t
	ix.registerSubject(t)
	ix.attach(t, nodeKey, m.Date)
}

// Ingest groups m, calling newThread when a brand-new conversation is seen.
// newThread receives the thread's normalized subject, persists it, and
// returns its database id. The result reports which thread the message
// belongs to and which existing threads were merged into it; for every id in
// MergedInto the caller must re-point that thread's messages onto
// Result.ThreadID.
func (ix *Index) Ingest(m Meta, newThread func(subject string) (int64, error)) (Result, error) {
	if newThread == nil {
		return Result{}, errors.New("thread: nil new-thread callback")
	}

	chain := m.ancestorChain()
	id := normalizeMessageID(m.MessageID)
	nodeKey := nodeKeyFor(m)

	var (
		candidates []*threadState
		seen       = make(map[*threadState]bool)
		rootKey    string
	)
	consider := func(key string) {
		t, ok := ix.threads[key]
		if !ok || seen[t] {
			return
		}
		seen[t] = true
		candidates = append(candidates, t)
	}

	// A thread already anchored to the message's own id: a duplicate copy of
	// the message, or replies that arrived before the message itself.
	if id != "" {
		consider(midPrefix + id)
	}
	// Threads holding known ancestors, plus placeholder threads keyed by
	// ancestors that are still missing.
	for _, ancestor := range chain {
		if threadKey, ok := ix.nodes[midPrefix+ancestor]; ok {
			consider(threadKey)
		}
		consider(midPrefix + ancestor)
	}
	// The chain root anchors the conversation whether or not that message has
	// arrived yet; missing non-root ancestors contribute nothing on their own.
	if len(chain) > 0 {
		rootKey = ix.resolveRootKey(chain)
		consider(rootKey)
	} else if subject := NormalizeSubject(m.Subject); subject != "" {
		// Without a usable chain, fall back to normalized subject within the
		// rolling window of each candidate thread's members.
		for _, key := range ix.bySubject[subject] {
			if t, ok := ix.threads[key]; ok && ix.withinWindow(t, m.Date) {
				consider(key)
			}
		}
	}

	// Elect the conversation the message joins. A message already in the
	// graph — a duplicate copy — keeps its current thread no matter what its
	// headers claim this time, and threads its chain implicates fold into it.
	// Otherwise the chain root anchors the conversation; without a chain the
	// message's own id thread wins, then the oldest subject match.
	var survivor *threadState
	survivorKey := ""
	if threadKey, ok := ix.nodes[nodeKey]; ok {
		if t, ok := ix.threads[threadKey]; ok {
			survivor, survivorKey = t, t.key
		}
	}
	if survivor == nil {
		survivorKey = rootKey
		switch {
		case survivorKey != "":
			survivor = ix.threads[survivorKey]
		case len(candidates) > 0:
			// Discovery order puts the message's own id thread first, then
			// subject matches oldest conversation first, so the survivor is
			// stable across repeated ingests.
			survivor = candidates[0]
			survivorKey = survivor.key
		}
	}
	if survivor == nil && survivorKey == "" {
		ix.seq++
		survivorKey = fmt.Sprintf("s%d", ix.seq)
	}

	var result Result
	if survivor == nil {
		subject := NormalizeSubject(m.Subject)
		dbID, err := newThread(subject)
		if err != nil {
			return Result{}, fmt.Errorf("thread: create thread: %w", err)
		}
		survivor = &threadState{
			key:     survivorKey,
			dbID:    dbID,
			subject: subject,
			aliases: []string{survivorKey},
		}
		ix.threads[survivorKey] = survivor
		ix.registerSubject(survivor)
	}
	result.ThreadID = survivor.dbID

	for _, t := range candidates {
		if t == survivor {
			continue
		}
		result.MergedInto = append(result.MergedInto, t.dbID)
		ix.mergeThreads(survivor, t)
	}

	ix.attach(survivor, nodeKey, m.Date)
	return result, nil
}

// mergeThreads folds absorbed into survivor: its member nodes are re-pointed,
// its date span and key aliases transfer, and it stops existing as its own
// conversation.
func (ix *Index) mergeThreads(survivor, absorbed *threadState) {
	for _, nodeKey := range absorbed.nodeKeys {
		if _, ok := ix.nodes[nodeKey]; !ok {
			continue
		}
		ix.nodes[nodeKey] = survivor.key
		survivor.nodeKeys = append(survivor.nodeKeys, nodeKey)
	}
	absorbed.nodeKeys = nil
	survivor.earliest = earlier(survivor.earliest, absorbed.earliest)
	survivor.latest = later(survivor.latest, absorbed.latest)
	if absorbed.subject != "" {
		ix.removeSubjectKey(absorbed.subject, absorbed.key)
	}
	// Keep every alias resolvable: future mail referencing any ancestor of
	// the absorbed conversation must land in the survivor rather than
	// resurrecting a separate thread.
	for _, alias := range absorbed.aliases {
		ix.threads[alias] = survivor
		survivor.aliases = append(survivor.aliases, alias)
	}
}

// attach records the message's node under t and widens the thread's date
// span. A node already present — a duplicate Message-ID — keeps its place, so
// a repeated copy joins its thread without counting twice.
func (ix *Index) attach(t *threadState, nodeKey string, date time.Time) {
	if _, known := ix.nodes[nodeKey]; known {
		return
	}
	ix.nodes[nodeKey] = t.key
	t.nodeKeys = append(t.nodeKeys, nodeKey)
	t.earliest = earlier(t.earliest, date)
	t.latest = later(t.latest, date)
}

// registerSubject makes t findable by the subject fallback. Threads without a
// usable normalized subject never participate: an empty subject carries no
// grouping signal.
func (ix *Index) registerSubject(t *threadState) {
	if t.subject == "" {
		return
	}
	ix.bySubject[t.subject] = append(ix.bySubject[t.subject], t.key)
}

// removeSubjectKey drops one thread key from the subject fallback index.
func (ix *Index) removeSubjectKey(subject, key string) {
	keys := ix.bySubject[subject]
	kept := keys[:0]
	for _, k := range keys {
		if k != key {
			kept = append(kept, k)
		}
	}
	if len(kept) == 0 {
		delete(ix.bySubject, subject)
		return
	}
	ix.bySubject[subject] = kept
}

// resolveRootKey returns the key the chain's root anchors the conversation
// under: the thread holding the root message when it is known, otherwise the
// placeholder key derived from the root Message-ID. Placeholder threads keep
// the seat warm so the late-arriving root joins the conversation it spawned.
func (ix *Index) resolveRootKey(chain []string) string {
	if threadKey, ok := ix.nodes[midPrefix+chain[0]]; ok {
		return threadKey
	}
	return midPrefix + chain[0]
}

// threadByDBID finds the conversation registered under a database id, used
// during hydration so every persisted member of one thread shares one graph
// state.
func (ix *Index) threadByDBID(dbID int64) *threadState {
	for _, t := range ix.threads {
		if t.dbID == dbID {
			return t
		}
	}
	return nil
}

// withinWindow reports whether a message dated d may join t under the
// subject fallback: its date must fall within the window of the thread's
// members. Unknown dates cannot be judged and never reject a match.
func (ix *Index) withinWindow(t *threadState, d time.Time) bool {
	if d.IsZero() || t.earliest.IsZero() || t.latest.IsZero() {
		return true
	}
	return !d.Before(t.earliest.Add(-ix.window)) && !d.After(t.latest.Add(ix.window))
}

// ancestorChain returns the normalized, de-duplicated ancestor Message-IDs in
// chain order (conversation root first), with In-Reply-To appended when the
// header names an ancestor the References header omits. The message's own id
// is excluded: self-referencing chains occur in broken mail.
func (m Meta) ancestorChain() []string {
	own := normalizeMessageID(m.MessageID)
	seen := make(map[string]struct{})
	var chain []string
	for _, raw := range m.References {
		chain = appendAncestor(chain, seen, raw, own)
	}
	return appendAncestor(chain, seen, m.InReplyTo, own)
}

// appendAncestor normalizes one candidate ancestor and appends it unless it
// is empty, a duplicate, or the message's own id.
func appendAncestor(chain []string, seen map[string]struct{}, raw, own string) []string {
	id := normalizeMessageID(raw)
	if id == "" || id == own {
		return chain
	}
	if _, dup := seen[id]; dup {
		return chain
	}
	seen[id] = struct{}{}
	return append(chain, id)
}

// nodeKeyFor is the graph address of a message: its normalized Message-ID
// when it has one, otherwise a row-scoped key. Messages without a Message-ID
// cannot be referenced by other mail, so they only ever join threads through
// their own chain or subject.
func nodeKeyFor(m Meta) string {
	if id := normalizeMessageID(m.MessageID); id != "" {
		return midPrefix + id
	}
	return rowPrefix + strconv.FormatInt(m.RowID, 10)
}

// normalizeMessageID trims whitespace and the surrounding angle brackets so
// headers written as "<a@b>" and " a@b " compare equal. Comparison stays
// case-sensitive: Message-IDs are.
func normalizeMessageID(raw string) string {
	id := strings.TrimSpace(raw)
	id = strings.TrimPrefix(id, "<")
	id = strings.TrimSuffix(id, ">")
	return strings.TrimSpace(id)
}

// earlier returns the earliest of the two times, treating the zero time as
// unset.
func earlier(a, b time.Time) time.Time {
	switch {
	case a.IsZero():
		return b
	case b.IsZero():
		return a
	case a.Before(b):
		return a
	default:
		return b
	}
}

// later returns the latest of the two times, treating the zero time as unset.
func later(a, b time.Time) time.Time {
	switch {
	case a.IsZero():
		return b
	case b.IsZero():
		return a
	case a.After(b):
		return a
	default:
		return b
	}
}
