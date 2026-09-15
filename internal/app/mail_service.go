package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mefiz0/posthaste/internal/mail"
	"github.com/mefiz0/posthaste/internal/search"
	"github.com/mefiz0/posthaste/internal/store"
)

// listPageSize is the message-list page size the bridge exposes; the frontend
// pages through with listMessages(account, view, page).
const listPageSize = 200

// searchCap bounds cross-account merged search results.
const searchCap = 200

// folderTypeOrder sorts folders in the sidebar's canonical order: the
// well-known mailboxes first, user folders last, names alphabetical within a
// group.
func folderTypeOrder(t store.FolderType) int {
	switch t {
	case store.FolderInbox:
		return 0
	case store.FolderDrafts:
		return 1
	case store.FolderSent:
		return 2
	case store.FolderArchive:
		return 3
	case store.FolderTrash:
		return 4
	case store.FolderJunk:
		return 5
	default:
		return 6
	}
}

// MailService is the bound read/write surface for folders, messages, flags,
// moves, and search. Every mutating call applies locally first (so the UI is
// instant and works offline), then queues the durable action and wakes the
// sync worker to replay and reconcile it.
type MailService struct {
	manager *Manager
}

// NewMailService returns the mail service bound to the manager.
func NewMailService(manager *Manager) *MailService {
	return &MailService{manager: manager}
}

// runtimeFor resolves the target runtimes for a call whose account ID is
// optional: zero means every running account.
func (s *MailService) runtimesFor(accountID int64) ([]*accountRuntime, error) {
	if accountID == 0 {
		return s.manager.Runtimes(), nil
	}
	rt, err := s.manager.Runtime(accountID)
	if err != nil {
		return nil, err
	}
	return []*accountRuntime{rt}, nil
}

// ListFolders returns the folder set for one account, or for every account
// when the ID is zero. Folder IDs are globally unique across accounts.
func (s *MailService) ListFolders(ctx context.Context, accountID int64) ([]FolderInfo, error) {
	runtimes, err := s.runtimesFor(accountID)
	if err != nil {
		return nil, err
	}
	out := make([]FolderInfo, 0)
	for _, rt := range runtimes {
		folders, err := rt.store.ListFolders(ctx)
		if err != nil {
			return nil, err
		}
		for _, f := range folders {
			out = append(out, folderInfoFor(rt, f))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		oi, oj := folderTypeOrder(store.FolderType(out[i].Type)), folderTypeOrder(store.FolderType(out[j].Type))
		if oi != oj {
			return oi < oj
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// folderInfoFor maps a stored folder onto the bridge shape with a global ID.
func folderInfoFor(rt *accountRuntime, f store.Folder) FolderInfo {
	return FolderInfo{
		ID:          globalFolderID(rt.entry.Ordinal, f.ID),
		AccountID:   rt.entry.ID,
		Name:        f.Name,
		IMAPPath:    f.IMAPPath,
		Type:        string(f.Type),
		UnreadCount: f.UnreadCount,
		TotalCount:  f.TotalCount,
	}
}

// ListMessages returns one page of summaries for a folder view, the starred
// view, or the unified inbox (no folder and no special view).
func (s *MailService) ListMessages(ctx context.Context, accountID int64, view MessageView, page int) ([]MessageSummaryInfo, error) {
	offset := page * listPageSize
	if offset < 0 {
		offset = 0
	}

	switch {
	case view.Special == "starred":
		runtimes, err := s.runtimesFor(accountID)
		if err != nil {
			return nil, err
		}
		var merged []MessageSummaryInfo
		for _, rt := range runtimes {
			messages, err := rt.store.ListMessages(ctx, store.MessageQuery{
				StarredOnly: true,
				Limit:       listPageSize,
				Offset:      offset,
			})
			if err != nil {
				return nil, err
			}
			for _, m := range messages {
				merged = append(merged, summaryFor(rt, m))
			}
		}
		sortByDate(merged)
		return capResults(merged, listPageSize), nil

	case view.FolderID > 0:
		rt, folderID, err := s.runtimeForFolder(accountID, view.FolderID)
		if err != nil {
			return nil, err
		}
		messages, err := rt.store.ListMessages(ctx, store.MessageQuery{
			FolderID: folderID,
			Limit:    listPageSize,
			Offset:   offset,
		})
		if err != nil {
			return nil, err
		}
		out := make([]MessageSummaryInfo, 0, len(messages))
		for _, m := range messages {
			out = append(out, summaryFor(rt, m))
		}
		return out, nil

	default:
		// The unified inbox: every account's inbox merged, newest first.
		runtimes, err := s.runtimesFor(accountID)
		if err != nil {
			return nil, err
		}
		var merged []MessageSummaryInfo
		for _, rt := range runtimes {
			inbox, err := rt.store.FolderByType(ctx, store.FolderInbox)
			if err != nil {
				continue
			}
			messages, err := rt.store.ListMessages(ctx, store.MessageQuery{
				FolderID: inbox.ID,
				Limit:    listPageSize,
				Offset:   offset,
			})
			if err != nil {
				return nil, err
			}
			for _, m := range messages {
				merged = append(merged, summaryFor(rt, m))
			}
		}
		sortByDate(merged)
		return capResults(merged, listPageSize), nil
	}
}

// runtimeForFolder resolves which runtime owns a global folder ID. An
// explicit account ID must agree with the folder's account.
func (s *MailService) runtimeForFolder(accountID, globalFolderID int64) (*accountRuntime, int64, error) {
	ordinal, localID := decodeFolderID(globalFolderID)
	if ordinal <= 0 || localID <= 0 {
		return nil, 0, fmt.Errorf("app: unknown folder %d", globalFolderID)
	}
	if accountID != 0 {
		rt, err := s.manager.Runtime(accountID)
		if err != nil {
			return nil, 0, err
		}
		if rt.entry.Ordinal != ordinal {
			return nil, 0, fmt.Errorf("app: folder %d does not belong to account %d", globalFolderID, accountID)
		}
		return rt, localID, nil
	}
	for _, rt := range s.manager.Runtimes() {
		if rt.entry.Ordinal == ordinal {
			return rt, localID, nil
		}
	}
	return nil, 0, fmt.Errorf("app: folder %d has no running account", globalFolderID)
}

// GetMessage returns one message's full detail, attachments included.
// Marking it read is a separate flags call by the frontend.
func (s *MailService) GetMessage(ctx context.Context, accountID, messageID int64) (MessageDetailInfo, error) {
	rt, err := s.manager.Runtime(accountID)
	if err != nil {
		return MessageDetailInfo{}, err
	}
	message, err := rt.store.MessageByID(ctx, messageID)
	if err != nil {
		return MessageDetailInfo{}, err
	}
	attachments, err := rt.store.ListAttachments(ctx, messageID)
	if err != nil {
		return MessageDetailInfo{}, err
	}
	detail := detailFor(rt, message, attachments)
	detail.HasRemoteContent = s.messageHasRemoteContent(ctx, rt, message)
	return detail, nil
}

// SetFlags applies a seen/starred patch optimistically, records the change in
// the durable offline queue, and wakes the sync worker to replay it.
func (s *MailService) SetFlags(ctx context.Context, accountID, messageID int64, patch FlagPatch) error {
	rt, err := s.manager.Runtime(accountID)
	if err != nil {
		return err
	}
	message, err := rt.store.MessageByID(ctx, messageID)
	if err != nil {
		return err
	}

	var add, remove store.Flags
	if patch.Seen != nil {
		if *patch.Seen {
			add = add.With(store.FlagSeen)
		} else {
			remove = remove.With(store.FlagSeen)
		}
	}
	if patch.Flagged != nil {
		if *patch.Flagged {
			add = add.With(store.FlagFlagged)
		} else {
			remove = remove.With(store.FlagFlagged)
		}
	}
	if add == 0 && remove == 0 {
		return nil
	}

	if err := rt.store.ApplyFlagDelta(ctx, message.ID, add, remove); err != nil {
		return err
	}
	kind := store.ActionUnflag
	if add != 0 {
		kind = store.ActionFlag
	}
	if _, err := rt.store.EnqueueAction(ctx, store.Action{
		Kind:        kind,
		MessageID:   message.ID,
		UID:         message.UID,
		FolderID:    message.FolderID,
		AddFlags:    add,
		RemoveFlags: remove,
	}); err != nil {
		return err
	}

	globalID := globalFolderID(rt.entry.Ordinal, message.FolderID)
	s.manager.Emit(EventMessagesChanged, MessagesChangedEvent{
		Type: EventMessagesChanged, AccountID: rt.entry.ID, FolderID: globalID,
	})
	if patch.Seen != nil {
		if unread, err := rt.store.CountUnread(ctx, message.FolderID); err == nil {
			s.manager.Emit(EventUnreadCount, UnreadCountEvent{
				Type: EventUnreadCount, AccountID: rt.entry.ID,
				FolderID: globalID, UnreadCount: unread,
			})
		}
	}
	if folder, err := rt.store.FolderByID(ctx, message.FolderID); err == nil {
		rt.triggerFolderSync(folder.IMAPPath)
	}
	return nil
}

// ArchiveMessage moves a message to the account's archive folder.
func (s *MailService) ArchiveMessage(ctx context.Context, accountID, messageID int64) error {
	rt, err := s.manager.Runtime(accountID)
	if err != nil {
		return err
	}
	target, err := rt.store.FolderByType(ctx, store.FolderArchive)
	if err != nil {
		return fmt.Errorf("app: the account has no archive folder")
	}
	return s.moveToFolder(ctx, rt, messageID, target, false)
}

// DeleteMessage moves a message to trash, or expunges it when it already
// sits in the trash folder.
func (s *MailService) DeleteMessage(ctx context.Context, accountID, messageID int64) error {
	rt, err := s.manager.Runtime(accountID)
	if err != nil {
		return err
	}
	message, err := rt.store.MessageByID(ctx, messageID)
	if err != nil {
		return err
	}
	current, err := rt.store.FolderByID(ctx, message.FolderID)
	if err != nil {
		return err
	}
	if current.Type == store.FolderTrash {
		return s.expungeMessage(ctx, rt, message)
	}
	target, err := rt.store.FolderByType(ctx, store.FolderTrash)
	if err != nil {
		return fmt.Errorf("app: the account has no trash folder")
	}
	return s.moveToFolder(ctx, rt, messageID, target, false)
}

// MoveMessage moves a message to the folder with the given global ID.
func (s *MailService) MoveMessage(ctx context.Context, accountID, messageID, targetFolderID int64) error {
	rt, targetID, err := s.runtimeForFolder(accountID, targetFolderID)
	if err != nil {
		return err
	}
	target, err := rt.store.FolderByID(ctx, targetID)
	if err != nil {
		return err
	}
	return s.moveToFolder(ctx, rt, messageID, target, false)
}

// moveToFolder applies a move optimistically, queues it durably, and wakes
// the sync worker.
func (s *MailService) moveToFolder(ctx context.Context, rt *accountRuntime, messageID int64, target store.Folder, _ bool) error {
	message, err := rt.store.MessageByID(ctx, messageID)
	if err != nil {
		return err
	}
	if message.FolderID == target.ID {
		return nil
	}
	if err := rt.store.MoveMessage(ctx, message.ID, target.ID); err != nil {
		return err
	}
	if _, err := rt.store.EnqueueAction(ctx, store.Action{
		Kind:           store.ActionMove,
		MessageID:      message.ID,
		UID:            message.UID,
		FolderID:       message.FolderID,
		TargetFolderID: target.ID,
	}); err != nil {
		return err
	}
	s.emitMessageChanged(rt, target.ID)
	rt.triggerFolderSync(target.IMAPPath)
	return nil
}

// expungeMessage permanently removes a message already in trash, locally and
// on the server via the queued action.
func (s *MailService) expungeMessage(ctx context.Context, rt *accountRuntime, message store.Message) error {
	if err := rt.store.DeleteMessage(ctx, message.ID); err != nil {
		return err
	}
	if _, err := rt.store.EnqueueAction(ctx, store.Action{
		Kind:      store.ActionDelete,
		MessageID: message.ID,
		UID:       message.UID,
		FolderID:  message.FolderID,
	}); err != nil {
		return err
	}
	s.emitMessageChanged(rt, message.FolderID)
	if folder, err := rt.store.FolderByID(ctx, message.FolderID); err == nil {
		rt.triggerFolderSync(folder.IMAPPath)
	}
	return nil
}

// MarkAllRead marks every message in a folder seen, queueing the server-side
// flag changes for replay.
func (s *MailService) MarkAllRead(ctx context.Context, accountID, folderGlobalID int64) error {
	rt, folderID, err := s.runtimeForFolder(accountID, folderGlobalID)
	if err != nil {
		return err
	}
	unread, err := rt.store.ListMessages(ctx, store.MessageQuery{
		FolderID:   folderID,
		UnreadOnly: true,
		Limit:      listPageSize * 4,
	})
	if err != nil {
		return err
	}
	for _, m := range unread {
		if _, err := rt.store.EnqueueAction(ctx, store.Action{
			Kind:      store.ActionRead,
			MessageID: m.ID,
			UID:       m.UID,
			FolderID:  m.FolderID,
		}); err != nil {
			return err
		}
	}
	if err := rt.store.MarkAllRead(ctx, folderID); err != nil {
		return err
	}

	if count, err := rt.store.CountUnread(ctx, folderID); err == nil {
		s.manager.Emit(EventUnreadCount, UnreadCountEvent{
			Type: EventUnreadCount, AccountID: rt.entry.ID,
			FolderID: folderGlobalID, UnreadCount: count,
		})
	}
	s.emitMessageChanged(rt, folderID)
	if folder, err := rt.store.FolderByID(ctx, folderID); err == nil {
		rt.triggerFolderSync(folder.IMAPPath)
	}
	return nil
}

// Search parses the filter (including the free-text operator syntax), runs
// the query per account, and merges the results newest first.
func (s *MailService) Search(ctx context.Context, accountID int64, filter SearchFilterInput) ([]MessageSummaryInfo, error) {
	runtimes, err := s.runtimesFor(accountID)
	if err != nil {
		return nil, err
	}
	parsed := search.Parse(filter.Text)

	storeFilter := store.SearchFilter{
		Text:    parsed.Text,
		From:    appendString(parsed.From, filter.From),
		To:      appendString(parsed.To, filter.To),
		Subject: appendString(parsed.Subject, filter.Subject),
		Limit:   searchCap,
	}
	if parsed.HasAttachment != nil && *parsed.HasAttachment || truthy(filter.HasAttachment) {
		yes := true
		storeFilter.HasAttachment = &yes
	}
	if parsed.IsUnread != nil && *parsed.IsUnread || truthy(filter.IsUnread) {
		yes := true
		storeFilter.IsUnread = &yes
	}
	if parsed.IsStarred != nil && *parsed.IsStarred || truthy(filter.IsStarred) {
		yes := true
		storeFilter.IsStarred = &yes
	}
	if t, ok := parseISO(filter.AfterISO); ok {
		storeFilter.After = t
	} else if parsed.After != nil {
		storeFilter.After = parsed.After.Unix()
	}
	if t, ok := parseISO(filter.BeforeISO); ok {
		storeFilter.Before = t
	} else if parsed.Before != nil {
		storeFilter.Before = parsed.Before.Unix()
	}

	folderName := filter.FolderName
	if folderName == "" {
		folderName = parsed.Folder
	}

	var merged []MessageSummaryInfo
	termless := storeFilter.Text == "" &&
		len(storeFilter.From) == 0 && len(storeFilter.To) == 0 && len(storeFilter.Subject) == 0
	for _, rt := range runtimes {
		query := storeFilter
		if folderName != "" {
			folderID, found := folderIDByName(ctx, rt, folderName)
			if !found {
				continue
			}
			query.FolderID = folderID
		}
		var messages []store.Message
		if termless {
			// The FTS index cannot match an empty query; a termless search is
			// a pure attribute filter, applied over a bounded local scan.
			messages, err = scanMessagesByFilter(ctx, rt, query)
			if err != nil {
				return nil, err
			}
		} else {
			messages, err = rt.store.Search(ctx, query)
			if err != nil {
				return nil, err
			}
		}
		for _, m := range messages {
			merged = append(merged, summaryFor(rt, m))
		}
	}
	sortByDate(merged)
	return capResults(merged, searchCap), nil
}

// scanMessagesByFilter applies an attribute-only search filter over a bounded
// scan of the account's folders.
func scanMessagesByFilter(ctx context.Context, rt *accountRuntime, query store.SearchFilter) ([]store.Message, error) {
	folders, err := rt.store.ListFolders(ctx)
	if err != nil {
		return nil, err
	}
	var out []store.Message
	for _, folder := range folders {
		if query.FolderID > 0 && folder.ID != query.FolderID {
			continue
		}
		messages, err := rt.store.ListMessages(ctx, store.MessageQuery{
			FolderID:    folder.ID,
			UnreadOnly:  query.IsUnread != nil && *query.IsUnread,
			StarredOnly: query.IsStarred != nil && *query.IsStarred,
			Limit:       searchCap,
		})
		if err != nil {
			return nil, err
		}
		for _, m := range messages {
			if matchesSearchFilter(m, query) {
				out = append(out, m)
			}
		}
	}
	return out, nil
}

// matchesSearchFilter reports whether a message satisfies the attribute parts
// of a search filter.
func matchesSearchFilter(m store.Message, query store.SearchFilter) bool {
	if query.HasAttachment != nil && *query.HasAttachment != m.HasAttachments {
		return false
	}
	if query.IsUnread != nil && *query.IsUnread == m.Flags.Has(store.FlagSeen) {
		return false
	}
	if query.IsStarred != nil && *query.IsStarred && !m.Flags.Has(store.FlagFlagged) {
		return false
	}
	if query.After > 0 && m.Date.Unix() < query.After {
		return false
	}
	if query.Before > 0 && m.Date.Unix() > query.Before {
		return false
	}
	return true
}

// folderIDByName resolves a folder by case-insensitive name within one
// account, accepting substring matches.
func folderIDByName(ctx context.Context, rt *accountRuntime, name string) (int64, bool) {
	folders, err := rt.store.ListFolders(ctx)
	if err != nil {
		return 0, false
	}
	needle := strings.ToLower(name)
	for _, f := range folders {
		if strings.EqualFold(f.Name, name) {
			return f.ID, true
		}
	}
	for _, f := range folders {
		if strings.Contains(strings.ToLower(f.Name), needle) {
			return f.ID, true
		}
	}
	return 0, false
}

// ListThreads returns one page of conversation summaries for an account,
// optionally restricted to one folder.
func (s *MailService) ListThreads(ctx context.Context, accountID, folderGlobalID int64) ([]ThreadSummaryInfo, error) {
	rt, err := s.manager.Runtime(accountID)
	if err != nil {
		return nil, err
	}
	var folderID int64
	if folderGlobalID > 0 {
		if _, local, err := s.runtimeForFolder(accountID, folderGlobalID); err == nil {
			folderID = local
		}
	}

	threads, err := rt.store.ListThreads(ctx, listPageSize, 0)
	if err != nil {
		return nil, err
	}
	out := make([]ThreadSummaryInfo, 0, len(threads))
	for _, t := range threads {
		messages, err := rt.store.ListMessagesByThread(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		summary, ok := threadSummaryFor(t, messages, folderID)
		if !ok {
			continue
		}
		out = append(out, summary)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastDateISO > out[j].LastDateISO })
	return out, nil
}

// GetThread returns every message of one conversation, oldest first.
func (s *MailService) GetThread(ctx context.Context, accountID, threadID int64) ([]MessageSummaryInfo, error) {
	rt, err := s.manager.Runtime(accountID)
	if err != nil {
		return nil, err
	}
	messages, err := rt.store.ListMessagesByThread(ctx, threadID)
	if err != nil {
		return nil, err
	}
	out := make([]MessageSummaryInfo, 0, len(messages))
	for _, m := range messages {
		out = append(out, summaryFor(rt, m))
	}
	return out, nil
}

// threadSummaryFor aggregates one thread's messages into the bridge shape,
// skipping threads that have no message in the requested folder.
func threadSummaryFor(t store.Thread, messages []store.Message, folderID int64) (ThreadSummaryInfo, bool) {
	var (
		subject      string
		lastDate     time.Time
		unread       int
		attachments  bool
		participants []string
		seen         = make(map[string]bool)
		count        int
	)
	for _, m := range messages {
		if folderID > 0 && m.FolderID != folderID {
			continue
		}
		count++
		if subject == "" {
			subject = m.Subject
		}
		if m.Date.After(lastDate) {
			lastDate = m.Date
		}
		if !m.Flags.Has(store.FlagSeen) {
			unread++
		}
		if m.HasAttachments {
			attachments = true
		}
		if m.FromName != "" && !seen[m.FromName] {
			seen[m.FromName] = true
			participants = append(participants, m.FromName)
		}
	}
	if count == 0 {
		return ThreadSummaryInfo{}, false
	}
	if subject == "" {
		subject = t.SubjectNormalized
	}
	return ThreadSummaryInfo{
		ID:             t.ID,
		Subject:        subject,
		MessageCount:   count,
		LastDateISO:    rfc3339(lastDate),
		UnreadCount:    unread,
		HasAttachments: attachments,
		Participants:   participants,
	}, true
}

// summaryFor maps a stored message onto the bridge summary shape.
func summaryFor(rt *accountRuntime, m store.Message) MessageSummaryInfo {
	return MessageSummaryInfo{
		ID:          m.ID,
		FolderID:    globalFolderID(rt.entry.Ordinal, m.FolderID),
		ThreadID:    m.ThreadID,
		FromName:    m.FromName,
		FromAddress: m.FromAddress,
		Subject:     m.Subject,
		DateISO:     rfc3339(m.Date),
		Flags: MessageFlagsInfo{
			Seen:     m.Flags.Has(store.FlagSeen),
			Flagged:  m.Flags.Has(store.FlagFlagged),
			Answered: m.Flags.Has(store.FlagAnswered),
			Draft:    m.Flags.Has(store.FlagDraft),
		},
		HasAttachments: m.HasAttachments,
		AccountColor:   rt.entry.Color,
		Snippet:        mail.Preview(m.BodyText, 96),
	}
}

// detailFor maps a stored message plus its attachments onto the bridge
// detail shape.
func detailFor(rt *accountRuntime, m store.Message, attachments []store.Attachment) MessageDetailInfo {
	detail := MessageDetailInfo{
		MessageSummaryInfo: summaryFor(rt, m),
		ToAddresses:        store.AddressList(m.ToAddresses),
		CCAddresses:        store.AddressList(m.CCAddresses),
		BodyText:           m.BodyText,
		BodyHTML:           m.BodyHTML,
		Attachments:        make([]AttachmentInfo, 0, len(attachments)),
		InReplyTo:          m.InReplyTo,
		References:         store.AddressList(m.References),
	}
	for _, a := range attachments {
		detail.Attachments = append(detail.Attachments, attachmentInfoFor(a))
	}
	return detail
}

// attachmentInfoFor maps a stored attachment onto the bridge shape.
func attachmentInfoFor(a store.Attachment) AttachmentInfo {
	return AttachmentInfo{
		ID:          a.ID,
		Filename:    a.Filename,
		MimeType:    a.MIMEType,
		SizeBytes:   a.SizeBytes,
		ContentHash: a.ContentHash,
		IsInline:    a.IsInline,
		ContentID:   a.ContentID,
		FetchState:  string(a.FetchState),
	}
}

// emitMessageChanged pushes the messages-changed event for one local folder.
func (s *MailService) emitMessageChanged(rt *accountRuntime, localFolderID int64) {
	s.manager.Emit(EventMessagesChanged, MessagesChangedEvent{
		Type:      EventMessagesChanged,
		AccountID: rt.entry.ID,
		FolderID:  globalFolderID(rt.entry.Ordinal, localFolderID),
	})
}

// sortByDate orders summaries newest first, oldest position as tie-breaker.
func sortByDate(summaries []MessageSummaryInfo) {
	sort.SliceStable(summaries, func(i, j int) bool {
		return summaries[i].DateISO > summaries[j].DateISO
	})
}

// capResults trims a merged list to limit entries.
func capResults(summaries []MessageSummaryInfo, limit int) []MessageSummaryInfo {
	if len(summaries) > limit {
		return summaries[:limit]
	}
	return summaries
}

// truthy dereferences an optional boolean filter field.
func truthy(v *bool) bool { return v != nil && *v }

// appendString appends a non-empty value to a filter term list.
func appendString(list []string, value string) []string {
	if strings.TrimSpace(value) == "" {
		return list
	}
	return append(list, value)
}

// parseISO reads an RFC 3339 timestamp from the bridge. It reports false for
// empty or malformed values rather than failing the whole search.
func parseISO(value string) (int64, bool) {
	if strings.TrimSpace(value) == "" {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return 0, false
	}
	return t.Unix(), true
}

// errNoAccount is returned when a compose call arrives with no account to
// send from.
var errNoAccount = errors.New("app: no account is available for this action")
