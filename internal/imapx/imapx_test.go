package imapx

import (
	"context"
	"testing"

	"github.com/emersion/go-imap/v2"
)

func TestUIDSetString(t *testing.T) {
	tests := []struct {
		name string
		set  UIDSet
		want string
	}{
		{name: "empty zero value", set: UIDSet{}, want: ""},
		{name: "single uid", set: UIDSingle(7), want: "7"},
		{
			name: "inclusive range",
			set:  UIDRangeSet(100, 200),
			want: "100:200",
		},
		{
			name: "star denotes last message",
			set:  UIDRangeSet(100, 0),
			want: "100:*",
		},
		{
			name: "single star",
			set:  UIDSingle(0),
			want: "*",
		},
		{
			name: "several entries",
			set: func() UIDSet {
				var s UIDSet
				s.Add(3)
				s.AddRange(10, 12)
				s.Add(42)
				return s
			}(),
			want: "3,10:12,42",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.set.String(); got != tc.want {
				t.Errorf("UIDSet.String() = %q, want %q", got, tc.want)
			}
			if tc.set.Empty() != (tc.want == "") {
				t.Errorf("UIDSet.Empty() = %v, want %v", tc.set.Empty(), tc.want == "")
			}
		})
	}
}

func TestUIDSetWireRendering(t *testing.T) {
	// The wire conversion must produce exactly the sequence set this package
	// advertises via String, since it is what goes on the socket.
	sets := []UIDSet{
		UIDSingle(1),
		UIDRangeSet(5, 9),
		UIDRangeSet(100, 0),
		func() UIDSet {
			var s UIDSet
			s.Add(2)
			s.Add(8)
			s.AddRange(20, 0)
			return s
		}(),
	}
	for _, set := range sets {
		want := set.String()
		if wire := set.wireSet().String(); wire != want {
			t.Errorf("wireSet(%q) rendered %q on the wire", want, wire)
		}
	}
}

func TestUIDSetDynamic(t *testing.T) {
	tests := []struct {
		name string
		set  UIDSet
		want bool
	}{
		{name: "static single", set: UIDSingle(4), want: false},
		{name: "static range", set: UIDRangeSet(4, 9), want: false},
		{name: "star upper bound", set: UIDRangeSet(4, 0), want: true},
		{name: "star single", set: UIDSingle(0), want: true},
		{
			name: "star among static ranges",
			set: func() UIDSet {
				var s UIDSet
				s.Add(1)
				s.AddRange(2, 0)
				return s
			}(),
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.set.Dynamic(); got != tc.want {
				t.Errorf("UIDSet.Dynamic() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUIDSetStaticForFetch(t *testing.T) {
	tests := []struct {
		name    string
		set     UIDSet
		largest uint32
		want    string
	}{
		{
			name:    "static set untouched",
			set:     UIDRangeSet(2, 5),
			largest: 10,
			want:    "2:5",
		},
		{
			name:    "lone star becomes the largest uid",
			set:     UIDSingle(0),
			largest: 7,
			want:    "7",
		},
		{
			name:    "n star clamps to largest",
			set:     UIDRangeSet(4, 0),
			largest: 9,
			want:    "4:9",
		},
		{
			name:    "lower bound beyond largest wraps to the newest message",
			set:     UIDRangeSet(100, 0),
			largest: 9,
			want:    "9:100",
		},
		{
			name:    "star as lower bound",
			set:     UIDRangeSet(0, 3),
			largest: 8,
			want:    "3:8",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.set.staticForFetch(tc.largest).String()
			if got != tc.want {
				t.Errorf("staticForFetch(%q, %d) = %q, want %q", tc.set, tc.largest, got, tc.want)
			}
		})
	}
}

func TestMailboxRole(t *testing.T) {
	tests := []struct {
		name  string
		attrs []string
		path  string
		delim string
		want  string
	}{
		{name: "sent attribute", attrs: []string{`\HasChildren`, `\Sent`}, path: "Sent items", want: RoleSent},
		{name: "drafts attribute", attrs: []string{`\Drafts`}, path: "Drafts", want: RoleDrafts},
		{name: "trash attribute", attrs: []string{`\Trash`}, path: "Deleted", want: RoleTrash},
		{name: "junk attribute", attrs: []string{`\Junk`}, path: "Spam", want: RoleJunk},
		{name: "archive attribute", attrs: []string{`\Archive`}, path: "Archiv", want: RoleArchive},
		{name: "all attribute maps to archive", attrs: []string{`\All`}, path: "All mail", want: RoleArchive},
		{name: "attribute is case insensitive", attrs: []string{`\sent`}, path: "whatever", want: RoleSent},
		{name: "unrelated attributes", attrs: []string{`\HasChildren`, `\Marked`}, path: "Projects/work", delim: "/", want: ""},
		{name: "inbox by name", path: "INBOX", want: RoleInbox},
		{name: "inbox name lowercase", path: "inbox", want: RoleInbox},
		{name: "sent by name", path: "Sent", want: RoleSent},
		{name: "junk by lowercase name", path: "junk", want: RoleJunk},
		{name: "name match on last element only", path: "Archive/2026", delim: "/", want: ""},
		{name: "nested sent with dot delimiter", path: "Root.Sent", delim: ".", want: RoleSent},
		{name: "nothing matches", path: "Projects/work", delim: "/", want: ""},
		{name: "no delimiter whole path considered", path: "Trash", want: RoleTrash},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := mailboxRole(tc.attrs, tc.path, tc.delim); got != tc.want {
				t.Errorf("mailboxRole(%q, %q, %q) = %q, want %q", tc.attrs, tc.path, tc.delim, got, tc.want)
			}
		})
	}
}

func TestHierarchyPrefixes(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		delim string
		want  []string
	}{
		{
			name:  "no delimiter known",
			path:  "Archive/2026",
			delim: "",
			want:  []string{"Archive/2026"},
		},
		{
			name:  "single element",
			path:  "INBOX",
			delim: "/",
			want:  []string{"INBOX"},
		},
		{
			name:  "nested with slash",
			path:  "Archive/2026/Music",
			delim: "/",
			want:  []string{"Archive", "Archive/2026", "Archive/2026/Music"},
		},
		{
			name:  "nested with dot",
			path:  "Root.Sent.Work",
			delim: ".",
			want:  []string{"Root", "Root.Sent", "Root.Sent.Work"},
		},
		{
			name:  "trailing delimiter yields the path once",
			path:  "Archive/",
			delim: "/",
			want:  []string{"Archive"},
		},
		{
			name:  "doubled delimiter skips empty elements",
			path:  "Archive//2026",
			delim: "/",
			want:  []string{"Archive", "Archive/2026"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := hierarchyPrefixes(tc.path, tc.delim)
			if len(got) != len(tc.want) {
				t.Fatalf("hierarchyPrefixes(%q, %q) = %v, want %v", tc.path, tc.delim, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("hierarchyPrefixes(%q, %q) = %v, want %v", tc.path, tc.delim, got, tc.want)
				}
			}
		})
	}
}

func TestLastPathElement(t *testing.T) {
	tests := []struct {
		path  string
		delim string
		want  string
	}{
		{path: "INBOX", delim: "", want: "INBOX"},
		{path: "INBOX", delim: "/", want: "INBOX"},
		{path: "Archive/2026", delim: "/", want: "2026"},
		{path: "Root.Sent", delim: ".", want: "Sent"},
		{path: "a//b", delim: "/", want: "b"},
	}
	for _, tc := range tests {
		if got := lastPathElement(tc.path, tc.delim); got != tc.want {
			t.Errorf("lastPathElement(%q, %q) = %q, want %q", tc.path, tc.delim, got, tc.want)
		}
	}
}

func TestMailboxExistsError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "structured ALREADYEXISTS code",
			err:  &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeAlreadyExists, Text: "Mailbox already exists"},
			want: true,
		},
		{
			name: "other response code with telling text",
			err:  &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeCannot, Text: "Mailbox already exists"},
			want: true,
		},
		{
			name: "plain server text without code",
			err:  &imap.Error{Type: imap.StatusResponseTypeNo, Text: "that mailbox is already existing, sorry"},
			want: true,
		},
		{
			name: "unrelated failure",
			err:  &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeNoPerm, Text: "permission denied"},
			want: false,
		},
		{
			name: "transport failure",
			err:  context.DeadlineExceeded,
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := mailboxExists(tc.err); got != tc.want {
				t.Errorf("mailboxExists(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
