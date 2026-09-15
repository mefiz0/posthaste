// Package store owns per-account persistence: the SQLite connection, embedded
// goose migrations, and repositories for folders, messages, attachments,
// threads, the offline action queue, and the send outbox.
//
// It is the lowest-level package in the core engine and imports no other
// internal package except settings (for the application path layout).
package store

import (
	"errors"
	"time"
)

// Sentinel errors returned by repositories.
var (
	ErrNotFound = errors.New("store: not found")
	ErrConflict = errors.New("store: conflict")
)

// FolderType classifies a folder so the UI and sync engine can treat the
// well-known mailboxes specially without relying on localised names.
type FolderType string

// Well-known folder types.
const (
	FolderInbox   FolderType = "inbox"
	FolderSent    FolderType = "sent"
	FolderDrafts  FolderType = "drafts"
	FolderArchive FolderType = "archive"
	FolderTrash   FolderType = "trash"
	FolderJunk    FolderType = "junk"
	FolderUser    FolderType = "user"
)

// Flags is a bitmask mirroring the IMAP flags the client tracks locally.
type Flags uint32

// Message flags.
const (
	FlagSeen     Flags = 1 << iota // \Seen
	FlagAnswered                   // \Answered
	FlagFlagged                    // \Flagged  (starred)
	FlagDraft                      // \Draft
	FlagDeleted                    // \Deleted
)

// Has reports whether every bit in mask is set.
func (f Flags) Has(mask Flags) bool { return f&mask == mask }

// With returns f with the given bits set.
func (f Flags) With(mask Flags) Flags { return f | mask }

// Without returns f with the given bits cleared.
func (f Flags) Without(mask Flags) Flags { return f &^ mask }

// Folder is a mailbox within an account.
type Folder struct {
	ID          int64
	Name        string
	IMAPPath    string
	Type        FolderType
	Delimiter   string
	UIDValidity uint32
	UIDNext     uint32
	TotalCount  int
	UnreadCount int
	Attributes  string
}

// Message is a stored message header plus its derived body fields. The raw MIME
// source lives on disk and is referenced by RawMIMEPath.
type Message struct {
	ID              int64
	FolderID        int64
	UID             uint32
	MessageIDHeader string
	InReplyTo       string
	References      string
	FromAddress     string
	FromName        string
	ToAddresses     string
	CCAddresses     string
	Subject         string
	Date            time.Time
	Flags           Flags
	SizeBytes       int64
	RawMIMEPath     string
	BodyText        string
	BodyHTML        string
	HasAttachments  bool
	ThreadID        int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// NewMessage is the subset of fields required to insert a message. Derived body
// fields may be filled later by the sync engine after MIME parsing.
type NewMessage struct {
	FolderID        int64
	UID             uint32
	MessageIDHeader string
	InReplyTo       string
	References      string
	FromAddress     string
	FromName        string
	ToAddresses     string
	CCAddresses     string
	Subject         string
	Date            time.Time
	Flags           Flags
	SizeBytes       int64
	RawMIMEPath     string
	BodyText        string
	BodyHTML        string
	HasAttachments  bool
	ThreadID        int64
}

// MessageQuery selects a page of messages for a folder.
type MessageQuery struct {
	FolderID int64
	// UnreadOnly restricts the result to unseen messages.
	UnreadOnly bool
	// StarredOnly restricts the result to flagged messages.
	StarredOnly bool
	Limit       int
	Offset      int
}

// Attachment is attachment metadata. The bytes live in the shared
// content-addressed store; StoragePath and ContentHash locate them.
type Attachment struct {
	ID             int64
	MessageID      int64
	Filename       string
	MIMEType       string
	SizeBytes      int64
	ContentHash    string
	StoragePath    string
	ContentID      string
	IsInline       bool
	FetchState     FetchState
	LastAccessedAt time.Time
}

// FetchState tracks whether an attachment's bytes are already local. Small
// attachments are fetched during sync; larger ones are fetched on demand.
type FetchState string

// Attachment fetch states.
const (
	FetchNotFetched FetchState = "not_fetched"
	FetchFetched    FetchState = "fetched"
)

// Thread groups messages into a conversation.
type Thread struct {
	ID                int64
	SubjectNormalized string
	LatestDate        time.Time
	MessageCount      int
}

// ActionKind enumerates the queued offline actions.
type ActionKind string

// Offline action kinds. Each is recorded durably before it is applied so a
// change made offline survives a restart and replays in order on reconnect.
const (
	ActionFlag    ActionKind = "flag"
	ActionUnflag  ActionKind = "unflag"
	ActionMove    ActionKind = "move"
	ActionDelete  ActionKind = "delete"
	ActionArchive ActionKind = "archive"
	ActionRead    ActionKind = "read"
	ActionUnread  ActionKind = "unread"
)

// Action is one entry in the durable offline action queue. Actions are replayed
// in ID order against the server on reconnect.
type Action struct {
	ID             int64
	Kind           ActionKind
	MessageID      int64
	UID            uint32
	FolderID       int64
	TargetFolderID int64
	AddFlags       Flags
	RemoveFlags    Flags
	CreatedAt      time.Time
	Attempts       int
	LastError      string
}

// SendState is the explicit lifecycle of an outgoing message. It is persisted so
// a composed message is never silently lost between restarts.
type SendState string

// Send lifecycle states.
const (
	SendDraft   SendState = "draft"
	SendQueued  SendState = "queued"
	SendSending SendState = "sending"
	SendSent    SendState = "sent"
	SendFailed  SendState = "failed"
)

// OutboxMessage is a composed message moving through the send pipeline.
type OutboxMessage struct {
	ID               string
	AccountID        string
	FromAddress      string
	FromName         string
	ToAddresses      string
	CCAddresses      string
	BCCAddresses     string
	Subject          string
	BodyText         string
	BodyHTML         string
	InReplyTo        string
	References       string
	AttachmentHashes string
	State            SendState
	Attempts         int
	LastError        string
	NextAttemptAt    time.Time
	RawMIMEPath      string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Contact is a derived autocomplete entry. It is never user-editable and is not
// exposed as an address book; it exists only to make recipient entry fast.
type Contact struct {
	ID             int64
	Email          string
	DisplayName    string
	LastUsedAt     time.Time
	FrequencyCount int
}

// Account holds the non-sensitive account settings. Secrets live only in the OS
// keyring; CredentialRef is the lookup key, never the secret itself.
type Account struct {
	ID                           string
	Email                        string
	DisplayName                  string
	IMAPHost                     string
	IMAPPort                     int
	IMAPTLS                      string
	IMAPUsername                 string
	SMTPHost                     string
	SMTPPort                     int
	SMTPTLS                      string
	SMTPUsername                 string
	AuthMethod                   string
	CredentialRef                string
	Signature                    string
	NotificationsEnabled         bool
	PollIntervalSeconds          int
	AttachmentEagerThresholdByte int64
	IsDefault                    bool
	Paused                       bool
	CreatedAt                    time.Time
	UpdatedAt                    time.Time
}
