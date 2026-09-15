package thread

import (
	"errors"
	"testing"
	"time"
)

// baseTime anchors every test date; deriving fixed offsets from one instant
// keeps the suite deterministic without sleeping.
var baseTime = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

// hook records newThread callback calls, handing out sequential ids starting
// above the ids tests hard-code for seeded threads.
type hook struct {
	nextID   int64
	subjects []string
	failWith error
}

func newHook() *hook { return &hook{nextID: 100} }

func (h *hook) create(subject string) (int64, error) {
	if h.failWith != nil {
		return 0, h.failWith
	}
	h.nextID++
	h.subjects = append(h.subjects, subject)
	return h.nextID, nil
}

// msg builds a Meta dated dayOffset days from baseTime.
func msg(row int64, id, inReplyTo string, refs []string, subject string, dayOffset int) Meta {
	return Meta{
		RowID:      row,
		MessageID:  id,
		InReplyTo:  inReplyTo,
		References: refs,
		Subject:    subject,
		Date:       baseTime.AddDate(0, 0, dayOffset),
	}
}

func ingest(t *testing.T, ix *Index, h *hook, m Meta) Result {
	t.Helper()
	result, err := ix.Ingest(m, h.create)
	if err != nil {
		t.Fatalf("Ingest(%q): %v", m.MessageID, err)
	}
	return result
}

func wantThreadID(t *testing.T, got, want int64, label string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: ThreadID = %d, want %d", label, got, want)
	}
}

func TestReferenceChainLinksReplies(t *testing.T) {
	ix := NewIndex(0)
	h := newHook()

	root := ingest(t, ix, h, msg(1, "<root@x>", "", nil, "Release plan", 0))
	wantThreadID(t, root.ThreadID, 101, "root")

	reply := ingest(t, ix, h, msg(2, "<reply@x>", "<root@x>", []string{"<root@x>"}, "Re: Release plan", 1))
	wantThreadID(t, reply.ThreadID, root.ThreadID, "reply")
	if len(reply.MergedInto) != 0 {
		t.Fatalf("reply merged %v, want none", reply.MergedInto)
	}

	deep := ingest(t, ix, h, msg(3, "<deep@x>", "<reply@x>",
		[]string{"<root@x>", "<reply@x>"}, "Re: Release plan", 2))
	wantThreadID(t, deep.ThreadID, root.ThreadID, "deep reply")

	if len(h.subjects) != 1 || h.subjects[0] != "release plan" {
		t.Fatalf("newThread subjects = %q, want one %q", h.subjects, "release plan")
	}
}

func TestLateAncestorMergesThreads(t *testing.T) {
	ix := NewIndex(0)
	h := newHook()

	// A standalone message with no references starts a subject thread.
	standalone := ingest(t, ix, h, msg(1, "<z@x>", "", nil, "Release plan", 0))
	// A reply to an unseen ancestor gets a placeholder thread keyed by that
	// ancestor.
	reply := ingest(t, ix, h, msg(2, "<r@x>", "<a@x>", []string{"<a@x>"}, "Re: Release plan", 1))
	if reply.ThreadID == standalone.ThreadID {
		t.Fatal("reply joined the subject thread before the ancestor existed")
	}

	// The ancestor itself arrives last: its own id thread wins and the
	// subject thread folds into it.
	root := ingest(t, ix, h, msg(3, "<a@x>", "", nil, "Re[2]: Release plan", 2))
	wantThreadID(t, root.ThreadID, reply.ThreadID, "late ancestor")
	if len(root.MergedInto) != 1 || root.MergedInto[0] != standalone.ThreadID {
		t.Fatalf("MergedInto = %v, want [%d]", root.MergedInto, standalone.ThreadID)
	}

	// The merged conversation stays one thread for later replies.
	follower := ingest(t, ix, h, msg(4, "<f@x>", "<a@x>",
		[]string{"<a@x>", "<r@x>"}, "Re: Release plan", 3))
	wantThreadID(t, follower.ThreadID, root.ThreadID, "post-merge reply")
	if len(follower.MergedInto) != 0 {
		t.Fatalf("post-merge reply merged %v, want none", follower.MergedInto)
	}
}

func TestChainMergesTwoAnchoredThreads(t *testing.T) {
	ix := NewIndex(0)
	h := newHook()

	// Two placeholder threads, each keyed by a different unseen anchor.
	first := ingest(t, ix, h, msg(1, "<m1@x>", "<a@x>", []string{"<a@x>"}, "topic", 0))
	second := ingest(t, ix, h, msg(2, "<m2@x>", "<b@x>", []string{"<b@x>"}, "topic", 1))
	if first.ThreadID == second.ThreadID {
		t.Fatal("placeholder threads collapsed before any chain linked them")
	}

	// A message whose chain spans both anchors unites the two threads under
	// the chain root's thread.
	link := ingest(t, ix, h, msg(3, "<m3@x>", "<b@x>",
		[]string{"<b@x>", "<a@x>"}, "Re: topic", 2))
	wantThreadID(t, link.ThreadID, second.ThreadID, "linking message")
	if len(link.MergedInto) != 1 || link.MergedInto[0] != first.ThreadID {
		t.Fatalf("MergedInto = %v, want [%d]", link.MergedInto, first.ThreadID)
	}

	// The absorbed anchor keeps resolving to the survivor instead of
	// resurrecting a separate thread.
	late := ingest(t, ix, h, msg(4, "<m4@x>", "<a@x>", []string{"<a@x>"}, "Re: topic", 3))
	wantThreadID(t, late.ThreadID, second.ThreadID, "message via absorbed anchor")
	if len(late.MergedInto) != 0 {
		t.Fatalf("late message merged %v, want none", late.MergedInto)
	}
}

func TestSubjectFallbackGroupsPrefixedSubjects(t *testing.T) {
	ix := NewIndex(0)
	h := newHook()

	first := ingest(t, ix, h, msg(1, "", "", nil, "Re: Fwd:  Quarterly   Report ", 0))
	second := ingest(t, ix, h, msg(2, "", "", nil, "quarterly report", 1))
	wantThreadID(t, second.ThreadID, first.ThreadID, "same subject without prefixes")

	third := ingest(t, ix, h, msg(3, "<c@x>", "", nil, "RE[2]: FWD: Quarterly Report", 2))
	wantThreadID(t, third.ThreadID, first.ThreadID, "same subject with numbered prefixes")

	if len(h.subjects) != 1 || h.subjects[0] != "quarterly report" {
		t.Fatalf("newThread subjects = %q, want one normalized subject", h.subjects)
	}
}

func TestSameSubjectOutsideWindowDoesNotMerge(t *testing.T) {
	ix := NewIndex(0) // default 30-day window
	h := newHook()

	first := ingest(t, ix, h, msg(1, "<j1@x>", "", nil, "Lunch?", 0))
	second := ingest(t, ix, h, msg(2, "<j2@x>", "", nil, "Re: Lunch?", 40))
	if second.ThreadID == first.ThreadID {
		t.Fatal("conversations 40 days apart merged")
	}

	// Inside the second conversation's window, outside the first: joins the
	// second only.
	third := ingest(t, ix, h, msg(3, "<j3@x>", "", nil, "Lunch?", 31))
	wantThreadID(t, third.ThreadID, second.ThreadID, "message 31 days in")

	fourth := ingest(t, ix, h, msg(4, "<j4@x>", "", nil, "Re: Lunch?", 80))
	if fourth.ThreadID == first.ThreadID || fourth.ThreadID == second.ThreadID {
		t.Fatal("conversation 80 days in joined an older one")
	}

	// Bridges the newest conversation's window from below.
	fifth := ingest(t, ix, h, msg(5, "<j5@x>", "", nil, "Lunch?", 75))
	wantThreadID(t, fifth.ThreadID, fourth.ThreadID, "message 75 days in")

	if len(h.subjects) != 3 {
		t.Fatalf("created %d threads, want 3", len(h.subjects))
	}
}

func TestSubjectMatchBridgesSplitThreads(t *testing.T) {
	ix := NewIndex(0)
	h := newHook()

	first := ingest(t, ix, h, msg(1, "<s1@x>", "", nil, "notes", 0))
	second := ingest(t, ix, h, msg(2, "<s2@x>", "", nil, "notes", 40))
	if second.ThreadID == first.ThreadID {
		t.Fatal("threads 40 days apart merged without a bridging message")
	}

	// A message dated within the window of both conversations unites them
	// under the older thread.
	bridge := ingest(t, ix, h, msg(3, "<s3@x>", "", nil, "Re: notes", 20))
	wantThreadID(t, bridge.ThreadID, first.ThreadID, "bridging message")
	if len(bridge.MergedInto) != 1 || bridge.MergedInto[0] != second.ThreadID {
		t.Fatalf("MergedInto = %v, want [%d]", bridge.MergedInto, second.ThreadID)
	}
}

func TestSubjectWindowIsConfigurable(t *testing.T) {
	cases := []struct {
		name       string
		window     time.Duration
		dayOffset  int
		sameThread bool
	}{
		{"one day apart joins a 48-hour window", 48 * time.Hour, 1, true},
		{"two days apart miss a 24-hour window", 24 * time.Hour, 2, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ix := NewIndex(tc.window)
			h := newHook()
			first := ingest(t, ix, h, msg(1, "<w1@x>", "", nil, "ping", 0))
			second := ingest(t, ix, h, msg(2, "<w2@x>", "", nil, "ping", tc.dayOffset))
			if tc.sameThread && second.ThreadID != first.ThreadID {
				t.Fatal("messages within the window landed in separate threads")
			}
			if !tc.sameThread && second.ThreadID == first.ThreadID {
				t.Fatal("messages outside the window landed in one thread")
			}
		})
	}
}

func TestEmptyAndMalformedMessageIDs(t *testing.T) {
	ix := NewIndex(0)
	h := newHook()

	// Messages without a Message-ID are addressable by row only: two of them
	// with different subjects never share a thread.
	alpha := ingest(t, ix, h, msg(1, "", "", nil, "alpha", 0))
	beta := ingest(t, ix, h, msg(2, "", "", nil, "beta", 0))
	if alpha.ThreadID == beta.ThreadID {
		t.Fatal("unrelated no-id messages shared a thread")
	}

	// One with only an In-Reply-To still lands in the placeholder thread for
	// its unseen root, which the root later joins.
	orphan := ingest(t, ix, h, msg(3, "", "<ghost@x>", nil, "Re: ghost", 1))
	ghost := ingest(t, ix, h, msg(4, "<ghost@x>", "", nil, "ghost", 2))
	wantThreadID(t, ghost.ThreadID, orphan.ThreadID, "late root of an orphaned reply")
	if len(ghost.MergedInto) != 0 {
		t.Fatalf("root merged %v, want none", ghost.MergedInto)
	}

	// Bracketed and padded ids normalize to the same nodes.
	padded := ingest(t, ix, h, msg(5, "  < ghost2@x >", "< ghost@x >",
		[]string{"  <ghost@x>  "}, "Re: ghost", 3))
	wantThreadID(t, padded.ThreadID, ghost.ThreadID, "padded headers")
}

func TestEmptySubjectNeverGroupsBySubject(t *testing.T) {
	ix := NewIndex(0)
	h := newHook()

	first := ingest(t, ix, h, msg(1, "", "", nil, "", 0))
	second := ingest(t, ix, h, msg(2, "", "", nil, "   ", 0))
	if second.ThreadID == first.ThreadID {
		t.Fatal("subjectless messages grouped by their empty subject")
	}
	if len(h.subjects) != 2 {
		t.Fatalf("created %d threads, want 2", len(h.subjects))
	}
}

func TestDuplicateMessageIDJoinsSameThread(t *testing.T) {
	ix := NewIndex(0)
	h := newHook()

	first := ingest(t, ix, h, msg(1, "<dup@x>", "", nil, "Status update", 0))
	second := ingest(t, ix, h, msg(2, "<dup@x>", "", nil, "Status update", 0))
	wantThreadID(t, second.ThreadID, first.ThreadID, "duplicate copy")

	// A third copy carrying a fuller chain keeps its thread too; the unseen
	// root in its References does not fork the conversation.
	third := ingest(t, ix, h, msg(3, "<dup@x>", "<dup@x>",
		[]string{"<root@x>", "<dup@x>"}, "Re: Status update", 1))
	wantThreadID(t, third.ThreadID, first.ThreadID, "duplicate with extended chain")
	if len(third.MergedInto) != 0 {
		t.Fatalf("duplicate merged %v, want none", third.MergedInto)
	}
	if len(h.subjects) != 1 {
		t.Fatalf("created %d threads, want 1", len(h.subjects))
	}

	// When the referenced root finally arrives it joins the same thread via
	// the subject fallback.
	root := ingest(t, ix, h, msg(4, "<root@x>", "", nil, "Status update", 0))
	wantThreadID(t, root.ThreadID, first.ThreadID, "referenced root")
}

func TestLongChainIngestedOutOfOrder(t *testing.T) {
	ix := NewIndex(0)
	h := newHook()

	fullChain := []string{"<a@x>", "<b@x>", "<c@x>"}
	// Every subject differs, so only the reference chain can link these; the
	// conversation root arrives last.
	ordered := []Meta{
		msg(1, "<d@x>", "<c@x>", fullChain, "delta", 3),
		msg(2, "<c@x>", "<b@x>", fullChain[:2], "gamma", 2),
		msg(3, "<b@x>", "<a@x>", fullChain[:1], "beta", 1),
		msg(4, "<a@x>", "", nil, "alpha", 0),
	}
	firstID := int64(0)
	for i, m := range ordered {
		result := ingest(t, ix, h, m)
		if i == 0 {
			firstID = result.ThreadID
			continue
		}
		wantThreadID(t, result.ThreadID, firstID, m.MessageID)
		if len(result.MergedInto) != 0 {
			t.Fatalf("%s merged %v, want none", m.MessageID, result.MergedInto)
		}
	}
	if len(h.subjects) != 1 {
		t.Fatalf("created %d threads, want 1", len(h.subjects))
	}
}

func TestSeedHydrationContinuesGrouping(t *testing.T) {
	ix := NewIndex(0)
	h := newHook()

	// Hydrate the persisted state: a reply, its late-arriving root, and a
	// subject-grouped companion, all persisted under thread 102.
	ix.Seed(msg(1, "<r@x>", "<a@x>", []string{"<a@x>"}, "Re: plan", 1), 102)
	ix.Seed(msg(2, "<z@x>", "", nil, "plan", 0), 102)
	ix.Seed(msg(3, "<a@x>", "", nil, "Re[2]: plan", 2), 102)

	follower := ingest(t, ix, h, msg(4, "<f@x>", "<a@x>",
		[]string{"<a@x>", "<r@x>"}, "Re: plan", 3))
	wantThreadID(t, follower.ThreadID, 102, "reply into hydrated thread")
	if len(h.subjects) != 0 {
		t.Fatalf("hydration ingest created threads with subjects %q", h.subjects)
	}

	// The subject fallback still finds hydrated threads.
	standalone := NewIndex(0)
	other := newHook()
	standalone.Seed(msg(1, "<p@x>", "", nil, "standup notes", 0), 201)
	second := ingest(t, standalone, other, msg(2, "<q@x>", "", nil, "Re: standup notes", 1))
	wantThreadID(t, second.ThreadID, 201, "subject match into hydrated thread")
	if len(other.subjects) != 0 {
		t.Fatalf("hydration ingest created threads with subjects %q", other.subjects)
	}
}

func TestSeedOrderIndependent(t *testing.T) {
	cases := []struct {
		name string
		seed func(*Index)
	}{
		{"root first", func(ix *Index) {
			ix.Seed(msg(1, "<a@x>", "", nil, "plan", 0), 7)
			ix.Seed(msg(2, "<r@x>", "<a@x>", []string{"<a@x>"}, "Re: plan", 1), 7)
		}},
		{"reply first", func(ix *Index) {
			ix.Seed(msg(2, "<r@x>", "<a@x>", []string{"<a@x>"}, "Re: plan", 1), 7)
			ix.Seed(msg(1, "<a@x>", "", nil, "plan", 0), 7)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ix := NewIndex(0)
			h := newHook()
			tc.seed(ix)
			result := ingest(t, ix, h, msg(3, "<n@x>", "<a@x>", []string{"<a@x>"}, "Re: plan", 2))
			wantThreadID(t, result.ThreadID, 7, "message after hydration")
			if len(h.subjects) != 0 {
				t.Fatalf("hydration ingest created threads with subjects %q", h.subjects)
			}
		})
	}
}

func TestNewThreadCallbackError(t *testing.T) {
	ix := NewIndex(0)
	boom := errors.New("store: insert thread: disk full")
	h := newHook()
	h.failWith = boom

	if _, err := ix.Ingest(msg(1, "<e@x>", "", nil, "hello", 0), h.create); !errors.Is(err, boom) {
		t.Fatalf("Ingest error = %v, want %v", err, boom)
	}

	if _, err := ix.Ingest(msg(2, "<n@x>", "", nil, "hello", 0), nil); err == nil {
		t.Fatal("Ingest with nil callback succeeded, want error")
	}
}

func TestNormalizeSubject(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "Hello world", "hello world"},
		{"reply prefix", "Re: Hello world", "hello world"},
		{"stacked prefixes", "Re: Fwd: Re[2]: FWD: Hello world", "hello world"},
		{"short forward", "Fw: Hello", "hello"},
		{"numbered reply", "Re[10]: Hello", "hello"},
		{"prefix needs a colon", "restart the server", "restart the server"},
		{"prefix must be a whole word", "reply to all: x", "reply to all: x"},
		{"list tag kept", "Re: [posthaste] Build broken", "[posthaste] build broken"},
		{"non-digit bracket is not a counter", "Re[abc]: Hello", "re[abc]: hello"},
		{"whitespace collapsed", "  Hello \t  world  ", "hello world"},
		{"empty", "", ""},
		{"only prefixes", "Re: Fw:", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeSubject(tc.in); got != tc.want {
				t.Fatalf("NormalizeSubject(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
