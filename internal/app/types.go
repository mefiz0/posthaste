// Package app is the Wails-facing application shell: it owns the account
// registry, the per-account runtimes (sync and send workers), and the bound
// services the frontend calls. It never imports Wails — main.go bridges the
// manager to the application's event bus, quit, and browser-open calls, so
// every piece here stays testable without a display.
package app

import (
	"strconv"
	"time"
)

// Bridge object names for the frontend's discriminated event union. The
// payload structs below carry the same string in their Type field so a single
// subscription can route them untouched.
const (
	EventSyncState       = "sync-state"
	EventSyncProgress    = "sync-progress"
	EventMessagesChanged = "messages-changed"
	EventFoldersChanged  = "folders-changed"
	EventSendState       = "send-state"
	EventUnreadCount     = "unread-count"
	EventToast           = "toast"
	EventAccountsChanged = "accounts-changed"
	EventSettingsChanged = "settings-changed"
	EventOAuthComplete   = "oauth-complete"
	EventUICompose       = "ui:compose"
)

// AccountInfo mirrors the frontend Account shape.
type AccountInfo struct {
	ID          int64  `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	IsDefault   bool   `json:"isDefault"`
	Paused      bool   `json:"paused"`
	Color       string `json:"color,omitempty"`
}

// FolderInfo mirrors the frontend Folder shape. ID is global across accounts:
// the low 32 bits carry the account-local folder row ID and the high bits the
// account ordinal, so the frontend can map any folder back to its account.
type FolderInfo struct {
	ID          int64  `json:"id"`
	AccountID   int64  `json:"accountId"`
	Name        string `json:"name"`
	IMAPPath    string `json:"imapPath"`
	Type        string `json:"type"`
	UnreadCount int    `json:"unreadCount"`
	TotalCount  int    `json:"totalCount"`
}

// MessageFlagsInfo mirrors the frontend MessageFlags shape.
type MessageFlagsInfo struct {
	Seen     bool `json:"seen"`
	Flagged  bool `json:"flagged"`
	Answered bool `json:"answered"`
	Draft    bool `json:"draft"`
}

// MessageSummaryInfo mirrors the frontend MessageSummary shape.
type MessageSummaryInfo struct {
	ID             int64            `json:"id"`
	FolderID       int64            `json:"folderId"`
	ThreadID       int64            `json:"threadId"`
	FromName       string           `json:"fromName"`
	FromAddress    string           `json:"fromAddress"`
	Subject        string           `json:"subject"`
	DateISO        string           `json:"dateIso"`
	Flags          MessageFlagsInfo `json:"flags"`
	HasAttachments bool             `json:"hasAttachments"`
	AccountColor   string           `json:"accountColor,omitempty"`
	Snippet        string           `json:"snippet"`
}

// AttachmentInfo mirrors the frontend Attachment shape.
type AttachmentInfo struct {
	ID          int64  `json:"id"`
	Filename    string `json:"filename"`
	MimeType    string `json:"mimeType"`
	SizeBytes   int64  `json:"sizeBytes"`
	ContentHash string `json:"contentHash"`
	IsInline    bool   `json:"isInline"`
	ContentID   string `json:"contentId,omitempty"`
	FetchState  string `json:"fetchState"`
}

// MessageDetailInfo mirrors the frontend MessageDetail shape.
type MessageDetailInfo struct {
	MessageSummaryInfo
	ToAddresses []string         `json:"toAddresses"`
	CCAddresses []string         `json:"ccAddresses"`
	BodyText    string           `json:"bodyText"`
	BodyHTML    string           `json:"bodyHtml,omitempty"`
	Attachments []AttachmentInfo `json:"attachments"`
	InReplyTo   string           `json:"inReplyTo,omitempty"`
	References  []string         `json:"references,omitempty"`
	// HasRemoteContent reports whether the message's HTML would load remote
	// resources if allowed, so the UI only offers the load-remote opt-in when
	// opting in changes anything.
	HasRemoteContent bool `json:"hasRemoteContent,omitempty"`
}

// ThreadSummaryInfo mirrors the frontend ThreadSummary shape.
type ThreadSummaryInfo struct {
	ID             int64    `json:"id"`
	Subject        string   `json:"subject"`
	MessageCount   int      `json:"messageCount"`
	LastDateISO    string   `json:"lastDateIso"`
	UnreadCount    int      `json:"unreadCount"`
	HasAttachments bool     `json:"hasAttachments"`
	Participants   []string `json:"participants"`
}

// ServerConfigInfo mirrors the frontend ServerConfig shape.
type ServerConfigInfo struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Security string `json:"security"`
	Username string `json:"username"`
}

// DiscoveredConfigInfo mirrors the frontend DiscoveredConfig shape. A nil
// result means nothing was discovered and the setup flow falls back to the
// manual form.
type DiscoveredConfigInfo struct {
	Email          string           `json:"email"`
	ProviderName   string           `json:"providerName"`
	RequiresOAuth  bool             `json:"requiresOAuth"`
	IMAP           ServerConfigInfo `json:"imap"`
	SMTP           ServerConfigInfo `json:"smtp"`
	AppPasswordURL string           `json:"appPasswordUrl,omitempty"`
}

// ManualAccountInput mirrors the frontend ManualAccountInput shape.
type ManualAccountInput struct {
	Email       string           `json:"email"`
	Password    string           `json:"password"`
	DisplayName string           `json:"displayName,omitempty"`
	Auth        string           `json:"auth"`
	IMAP        ServerConfigInfo `json:"imap"`
	SMTP        ServerConfigInfo `json:"smtp"`
}

// OAuthStart mirrors the first half of the frontend startOAuth result: the
// consent URL and the flow ID used to complete it.
type OAuthStart struct {
	AuthURL string `json:"url"`
	StateID string `json:"stateId"`
}

// OAuthResultInfo mirrors the frontend OAuthResult shape.
type OAuthResultInfo struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// OAuthCompleteEvent is emitted when a consent flow resolves, however it
// ends. The frontend awaits this event instead of holding a binding call
// open for the whole consent, and matches flows by StateID.
type OAuthCompleteEvent struct {
	Type    string `json:"type"`
	StateID string `json:"stateId"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
}

// PickedFile describes one file the user chose in the native attach dialog:
// the absolute path the engine reads from, plus the display metadata the
// compose drawer shows before the file is ingested.
type PickedFile struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	SizeBytes int64  `json:"sizeBytes"`
}

// MessageView mirrors the frontend MessageView shape.
type MessageView struct {
	FolderID int64  `json:"folderId,omitempty"`
	Special  string `json:"special,omitempty"`
}

// FlagPatch mirrors the frontend FlagPatch shape. A nil field means "leave
// unchanged".
type FlagPatch struct {
	Seen    *bool `json:"seen,omitempty"`
	Flagged *bool `json:"flagged,omitempty"`
}

// SearchFilterInput mirrors the frontend SearchFilter shape.
type SearchFilterInput struct {
	Text          string `json:"text"`
	From          string `json:"from,omitempty"`
	To            string `json:"to,omitempty"`
	Subject       string `json:"subject,omitempty"`
	HasAttachment *bool  `json:"hasAttachment,omitempty"`
	IsUnread      *bool  `json:"isUnread,omitempty"`
	IsStarred     *bool  `json:"isStarred,omitempty"`
	AfterISO      string `json:"afterIso,omitempty"`
	BeforeISO     string `json:"beforeIso,omitempty"`
	FolderName    string `json:"folderName,omitempty"`
}

// ContactInfo mirrors the frontend Contact shape.
type ContactInfo struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// DraftInput mirrors the frontend DraftInput shape.
type DraftInput struct {
	DraftID            string   `json:"draftId,omitempty"`
	AccountID          int64    `json:"accountId,omitempty"`
	InReplyToMessageID string   `json:"inReplyToMessageId,omitempty"`
	ToAddresses        []string `json:"toAddresses"`
	CCAddresses        []string `json:"ccAddresses"`
	BCCAddresses       []string `json:"bccAddresses"`
	Subject            string   `json:"subject"`
	BodyText           string   `json:"bodyText"`
	// Attachments holds absolute local paths, as returned by PickAttachments.
	// The engine reads them into the blob store and keeps the file names.
	Attachments []string `json:"attachments,omitempty"`
}

// DraftResult mirrors the frontend saveDraft result.
type DraftResult struct {
	ID string `json:"id"`
}

// SendResult mirrors the frontend sendDraft result.
type SendResult struct {
	Queued bool `json:"queued"`
}

// OutboxItem describes one outgoing message shown in the failed/retrying
// outbox view. The frontend gains this view later; the shape lives here so
// the binding is stable.
type OutboxItem struct {
	ID         int64  `json:"-"`
	OutboxID   string `json:"id"`
	AccountID  int64  `json:"accountId"`
	To         string `json:"to"`
	Subject    string `json:"subject"`
	State      string `json:"state"`
	Attempts   int    `json:"attempts"`
	Error      string `json:"error,omitempty"`
	CreatedISO string `json:"createdIso"`
}

// AppSettingsInfo mirrors the frontend AppSettings shape.
type AppSettingsInfo struct {
	NotificationsEnabled          bool              `json:"notificationsEnabled"`
	MinimizeToTray                bool              `json:"minimizeToTray"`
	VerboseLogging                bool              `json:"verboseLogging"`
	AttachmentEagerThresholdBytes int64             `json:"attachmentEagerThresholdBytes"`
	Keymap                        map[string]string `json:"keymap"`
}

// AccountPrefs carries the editable per-account preferences. A nil pointer
// leaves the stored value unchanged.
type AccountPrefs struct {
	DisplayName                   *string `json:"displayName,omitempty"`
	Signature                     *string `json:"signature,omitempty"`
	NotificationsEnabled          *bool   `json:"notificationsEnabled,omitempty"`
	PollIntervalSeconds           *int    `json:"pollIntervalSeconds,omitempty"`
	AttachmentEagerThresholdBytes *int64  `json:"attachmentEagerThresholdBytes,omitempty"`
}

// Event payload structs. Each carries its own Type so the frontend can route
// the whole stream through one discriminated union.

// SyncStateEvent is emitted whenever an account's sync condition changes.
type SyncStateEvent struct {
	Type      string `json:"type"`
	AccountID int64  `json:"accountId"`
	State     string `json:"state"`
	Detail    string `json:"detail,omitempty"`
}

// SyncProgressEvent reports one step of a sync pass so the UI can show a
// breakdown instead of an opaque "syncing". Folder is empty for pass-level
// steps; New is the messages fetched or changed in the step.
type SyncProgressEvent struct {
	Type      string `json:"type"`
	AccountID int64  `json:"accountId"`
	Folder    string `json:"folder,omitempty"`
	Phase     string `json:"phase"`
	New       int    `json:"new"`
	Total     int    `json:"total"`
	At        string `json:"at"`
}

// SyncActivityEntry is one retained sync activity line. The engine keeps a
// bounded buffer so the UI can show recent activity even when it subscribed
// after a pass had already run.
type SyncActivityEntry struct {
	At        string `json:"at"`
	AccountID int64  `json:"accountId"`
	Text      string `json:"text"`
	Level     string `json:"level"`
}

// FoldersChangedEvent is emitted after a sync pass reconciled the folder list.
type FoldersChangedEvent struct {
	Type      string `json:"type"`
	AccountID int64  `json:"accountId"`
}

// MessagesChangedEvent is emitted after messages in an account changed.
// FolderID, when set, is the global folder ID the change touched.
type MessagesChangedEvent struct {
	Type      string `json:"type"`
	AccountID int64  `json:"accountId"`
	FolderID  int64  `json:"folderId,omitempty"`
}

// SendStateEvent is emitted for every outbox state transition.
type SendStateEvent struct {
	Type      string `json:"type"`
	AccountID int64  `json:"accountId"`
	DraftID   string `json:"draftId"`
	State     string `json:"state"`
	Error     string `json:"error,omitempty"`
}

// UnreadCountEvent reports one folder's unseen total after a sync pass.
type UnreadCountEvent struct {
	Type        string `json:"type"`
	AccountID   int64  `json:"accountId"`
	FolderID    int64  `json:"folderId"`
	UnreadCount int    `json:"unreadCount"`
}

// ToastEvent is a transient user-facing notice.
type ToastEvent struct {
	Type    string `json:"type"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

// AccountsChangedEvent signals the account list changed.
type AccountsChangedEvent struct {
	Type string `json:"type"`
}

// SettingsChangedEvent carries the settings snapshot after a save.
type SettingsChangedEvent struct {
	Type     string          `json:"type"`
	Settings AppSettingsInfo `json:"settings"`
}

// rfc3339 formats a timestamp the way the bridge represents dates.
func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// accountIDString renders the bridge account ID in the string form the engine
// packages (store rows, sync/send event IDs, keyring refs) use.
func accountIDString(id int64) string {
	return strconv.FormatInt(id, 10)
}

// globalFolderID packs an account ordinal and the account-local folder row ID
// into one bridge-level folder ID. Folders live in per-account databases, so
// their local IDs collide across accounts; the packed form stays unique and
// lets the frontend map any folder back to its account.
func globalFolderID(ordinal int, localID int64) int64 {
	return int64(ordinal)<<32 | (localID & 0xFFFFFFFF)
}

// decodeFolderID splits a global folder ID back into its account ordinal and
// the account-local folder row ID.
func decodeFolderID(global int64) (ordinal int, localID int64) {
	return int(global >> 32), global & 0xFFFFFFFF
}
