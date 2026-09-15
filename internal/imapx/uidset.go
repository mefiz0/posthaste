package imapx

import (
	"strconv"
	"strings"

	"github.com/emersion/go-imap/v2"
)

// UIDSet identifies a group of messages by UID for fetch, flag, move, and
// expunge operations. The zero value is an empty set; build one with Add,
// AddRange, or the UIDSingle and UIDRangeSet constructors.
type UIDSet struct {
	ranges []uidRange
}

// uidRange is one inclusive run of UIDs. A bound of 0 denotes the last
// message, rendered as "*" in IMAP syntax.
type uidRange struct {
	from, to uint32
}

// UIDSingle returns a set containing the single message uid. The UID 0
// denotes the last message in the mailbox ("*").
func UIDSingle(uid uint32) UIDSet {
	var set UIDSet
	set.Add(uid)
	return set
}

// UIDRangeSet returns a set covering all UIDs from from to to, inclusive.
// A bound of 0 denotes the last message in the mailbox ("*").
func UIDRangeSet(from, to uint32) UIDSet {
	var set UIDSet
	set.AddRange(from, to)
	return set
}

// Add appends a single message UID to the set.
func (s *UIDSet) Add(uid uint32) {
	s.ranges = append(s.ranges, uidRange{from: uid, to: uid})
}

// AddRange appends all UIDs from from to to, inclusive, to the set.
func (s *UIDSet) AddRange(from, to uint32) {
	s.ranges = append(s.ranges, uidRange{from: from, to: to})
}

// String renders the set in IMAP sequence-set syntax, for example "7" or
// "100:200" or "3,100:200,*". An empty set renders as "".
func (s UIDSet) String() string {
	if len(s.ranges) == 0 {
		return ""
	}
	parts := make([]string, len(s.ranges))
	for i, r := range s.ranges {
		if r.from == r.to {
			parts[i] = formatUID(r.from)
		} else {
			parts[i] = formatUID(r.from) + ":" + formatUID(r.to)
		}
	}
	return strings.Join(parts, ",")
}

// formatUID renders one UID bound, mapping 0 to the IMAP wildcard for "the
// last message in the mailbox".
func formatUID(uid uint32) string {
	if uid == 0 {
		return "*"
	}
	return strconv.FormatUint(uint64(uid), 10)
}

// Empty reports whether the set selects no messages.
func (s UIDSet) Empty() bool {
	return len(s.ranges) == 0
}

// Dynamic reports whether any bound uses the IMAP wildcard "*" (the UID 0).
// A dynamic set is valid on the wire for every command, but FETCH responses
// are matched by the client library against the requested set, and a wildcard
// never matches a concrete UID there — so fetches must go through
// staticForFetch before going on the wire.
func (s UIDSet) Dynamic() bool {
	for _, r := range s.ranges {
		if r.from == 0 || r.to == 0 {
			return true
		}
	}
	return false
}

// staticForFetch returns a copy of the set with every "*" bound replaced by
// largest, the largest UID in use. The RFC treats "a:b" and "b:a" as
// equivalent, so a lower bound beyond the largest UID resolves down to the
// newest message, matching how servers interpret "n:*".
func (s UIDSet) staticForFetch(largest uint32) UIDSet {
	out := UIDSet{ranges: make([]uidRange, len(s.ranges))}
	for i, r := range s.ranges {
		from, to := r.from, r.to
		if from == 0 {
			from = largest
		}
		if to == 0 {
			to = largest
		}
		if from > to {
			from, to = to, from
		}
		out.ranges[i] = uidRange{from: from, to: to}
	}
	return out
}

// wireSet converts to the representation the IMAP wire commands expect.
func (s UIDSet) wireSet() imap.UIDSet {
	var out imap.UIDSet
	for _, r := range s.ranges {
		out.AddRange(imap.UID(r.from), imap.UID(r.to))
	}
	return out
}
