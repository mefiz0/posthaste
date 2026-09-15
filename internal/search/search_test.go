package search

import (
	"reflect"
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }

func TestParse(t *testing.T) {
	now := time.Date(2026, time.September, 14, 15, 30, 0, 0, time.UTC)
	date := func(y int, m time.Month, d int) *time.Time {
		return ptr(time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
	}
	tests := []struct {
		name  string
		input string
		want  Query
	}{
		{
			name:  "empty input is the zero query",
			input: "",
			want:  Query{},
		},
		{
			name:  "whitespace only is the zero query",
			input: "   \t  ",
			want:  Query{},
		},
		{
			name:  "free text only",
			input: "contract renewal",
			want:  Query{Text: "contract renewal"},
		},
		{
			name:  "quoted phrase in free text stays one term",
			input: `please review "quarterly report" now`,
			want:  Query{Text: "please review quarterly report now"},
		},
		{
			name:  "quoted phrase alone",
			input: `"quarterly report"`,
			want:  Query{Text: "quarterly report"},
		},
		{
			name:  "from operator",
			input: "from:sarah",
			want:  Query{From: []string{"sarah"}},
		},
		{
			name:  "from with quoted phrase",
			input: `from:"Sarah Connor"`,
			want:  Query{From: []string{"Sarah Connor"}},
		},
		{
			name:  "repeated from accumulates",
			input: "from:sarah from:bob",
			want:  Query{From: []string{"sarah", "bob"}},
		},
		{
			name:  "to operator",
			input: "to:team",
			want:  Query{To: []string{"team"}},
		},
		{
			name:  "subject operator",
			input: `subject:"quarterly report"`,
			want:  Query{Subject: []string{"quarterly report"}},
		},
		{
			name:  "has attachment",
			input: "has:attachment",
			want:  Query{HasAttachment: ptr(true)},
		},
		{
			name:  "unknown has value falls back to text",
			input: "has:image",
			want:  Query{Text: "has:image"},
		},
		{
			name:  "is unread",
			input: "is:unread",
			want:  Query{IsUnread: ptr(true)},
		},
		{
			name:  "is starred",
			input: "is:starred",
			want:  Query{IsStarred: ptr(true)},
		},
		{
			name:  "is flagged is starred",
			input: "is:flagged",
			want:  Query{IsStarred: ptr(true)},
		},
		{
			name:  "unknown is value falls back to text",
			input: "is:blue",
			want:  Query{Text: "is:blue"},
		},
		{
			name:  "in sets folder",
			input: "in:Archive",
			want:  Query{Folder: "Archive"},
		},
		{
			name:  "folder sets folder",
			input: "folder:Archive",
			want:  Query{Folder: "Archive"},
		},
		{
			name:  "absolute after date",
			input: "after:2024-01-02",
			want:  Query{After: date(2024, time.January, 2)},
		},
		{
			name:  "slash separated before date",
			input: "before:2024/03/04",
			want:  Query{Before: date(2024, time.March, 4)},
		},
		{
			name:  "relative today",
			input: "after:today",
			want:  Query{After: date(2026, time.September, 14)},
		},
		{
			name:  "relative yesterday",
			input: "before:yesterday",
			want:  Query{Before: date(2026, time.September, 13)},
		},
		{
			name:  "relative days",
			input: "after:7d",
			want:  Query{After: ptr(time.Date(2026, time.September, 7, 15, 30, 0, 0, time.UTC))},
		},
		{
			name:  "relative weeks",
			input: "after:2w",
			want:  Query{After: ptr(time.Date(2026, time.August, 31, 15, 30, 0, 0, time.UTC))},
		},
		{
			name:  "relative months",
			input: "before:1m",
			want:  Query{Before: ptr(time.Date(2026, time.August, 14, 15, 30, 0, 0, time.UTC))},
		},
		{
			name:  "relative years",
			input: "after:1y",
			want:  Query{After: ptr(time.Date(2025, time.September, 14, 15, 30, 0, 0, time.UTC))},
		},
		{
			name:  "malformed date falls back to text",
			input: "after:soon",
			want:  Query{Text: "after:soon"},
		},
		{
			name:  "unknown operator falls back to text",
			input: "foo:bar",
			want:  Query{Text: "foo:bar"},
		},
		{
			name:  "quoted colon is not an operator",
			input: `"foo:bar"`,
			want:  Query{Text: "foo:bar"},
		},
		{
			name:  "operator names are case insensitive",
			input: "FROM:sarah IS:UNREAD HAS:ATTACHMENT",
			want: Query{
				From:          []string{"sarah"},
				IsUnread:      ptr(true),
				HasAttachment: ptr(true),
			},
		},
		{
			name:  "mixed free text and operators",
			input: `from:sarah "quarterly report" has:attachment report`,
			want: Query{
				From:          []string{"sarah"},
				HasAttachment: ptr(true),
				Text:          "quarterly report report",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseWithClock(tt.input, now)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseWithClock(%q) = %+v, want %+v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseIsRead(t *testing.T) {
	got := Parse("is:read")
	if got.IsUnread == nil || *got.IsUnread {
		t.Fatalf("is:read should set IsUnread to false, got %+v", got.IsUnread)
	}
}

func TestParseNeverErrorsAndZeroValueMeansNoFilter(t *testing.T) {
	if got := Parse(""); !reflect.DeepEqual(got, Query{}) {
		t.Fatalf("empty parse should be zero Query, got %+v", got)
	}
	if got := Parse(":::: from: to: is: has:"); got.Text == "" {
		t.Fatalf("operator-like tokens without values should be text, got %+v", got)
	}
}
