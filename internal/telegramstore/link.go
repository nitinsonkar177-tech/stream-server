package telegramstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/gotd/td/tg"
)

var (
	// ErrLinkChannelNotFound marks a Telegram link whose channel cannot be
	// resolved with the importing user's own Telegram session (for example a
	// private channel the account is not a member of).
	ErrLinkChannelNotFound = errors.New("telegram channel not found or not accessible")
	// ErrLinkTargetNotChannel marks links whose username resolves to a user or
	// bot rather than a channel.
	ErrLinkTargetNotChannel = errors.New("telegram link does not point to a channel")
	// ErrProtectedContent marks messages or channels flagged as protected
	// ("saving content restricted"), and paid media. Such content is rejected
	// explicitly, never silently relied on to fail elsewhere.
	ErrProtectedContent = errors.New("telegram message is protected content")
)

// TelegramLinkRef identifies a Telegram channel message to resolve. Exactly
// one of ChannelID (private link form) or Username (public link form) is set.
type TelegramLinkRef struct {
	ChannelID int64
	Username  string
	MessageID int64
}

// ResolvedTelegramDocument is the media metadata of a resolved Telegram
// channel message.
type ResolvedTelegramDocument struct {
	ChannelID int64
	MessageID int64
	Size      int64
	MIMEType  string
	FileName  string
}

// LinkResolver is an optional Telegram capability. Production GotdStorage
// implements it; deterministic content-storage fakes do not need to.
type LinkResolver interface {
	ResolveTelegramLink(ctx context.Context, userID int64, ref TelegramLinkRef) (*ResolvedTelegramDocument, error)
}

// ResolveTelegramLink resolves a Telegram channel message link through the
// importing user's own stored Telegram session and returns the media metadata
// needed to register a catalog reference. It never downloads media bytes.
func (s *GotdStorage) ResolveTelegramLink(ctx context.Context, userID int64, ref TelegramLinkRef) (*ResolvedTelegramDocument, error) {
	if ref.MessageID <= 0 || (ref.ChannelID <= 0 && ref.Username == "") {
		return nil, ErrInvalidRequest
	}
	var resolved *ResolvedTelegramDocument
	err := s.runner.Run(ctx, userID, OperationDownload, func(runCtx context.Context, api *tg.Client) error {
		channel, err := linkChannel(runCtx, api, ref)
		if err != nil {
			return err
		}
		if channel.GetNoforwards() {
			return ErrProtectedContent
		}
		response, err := api.ChannelsGetMessages(runCtx, &tg.ChannelsGetMessagesRequest{
			Channel: channel.AsInput(),
			ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: int(ref.MessageID)}},
		})
		if err != nil {
			return fmt.Errorf("get Telegram link message: %w", err)
		}
		modified, ok := response.AsModified()
		if !ok || len(modified.GetMessages()) == 0 {
			return ErrMessageNotFound
		}
		msg, ok := modified.GetMessages()[0].(*tg.Message)
		if !ok {
			return ErrMessageNotFound
		}
		if msg.GetNoforwards() || hasPaidMedia(msg) {
			return ErrProtectedContent
		}
		size, mimeType, fileName, ok := documentMeta(msg)
		if !ok {
			return ErrDocumentNotFound
		}
		resolved = &ResolvedTelegramDocument{
			ChannelID: channel.GetID(),
			MessageID: ref.MessageID,
			Size:      size,
			MIMEType:  mimeType,
			FileName:  fileName,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return resolved, nil
}

func linkChannel(ctx context.Context, api *tg.Client, ref TelegramLinkRef) (*tg.Channel, error) {
	if ref.Username != "" {
		peer, err := api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: ref.Username})
		if err != nil {
			return nil, ErrLinkChannelNotFound
		}
		for _, chat := range peer.Chats {
			if channel, ok := chat.(*tg.Channel); ok {
				return channel, nil
			}
		}
		return nil, ErrLinkTargetNotChannel
	}
	channel, err := fullChannel(ctx, api, ref.ChannelID)
	if err != nil {
		return nil, ErrLinkChannelNotFound
	}
	return channel, nil
}

// hasPaidMedia reports whether the message carries paid (stars-gated) media.
func hasPaidMedia(msg *tg.Message) bool {
	media, ok := msg.GetMedia()
	if !ok {
		return false
	}
	documentMedia, ok := media.(*tg.MessageMediaDocument)
	if !ok {
		return false
	}
	return documentMedia.GetNopremium()
}

// documentMeta extracts size, MIME type and file name from a Telegram message
// carrying a document. It reports false for any other message shape.
func documentMeta(msg *tg.Message) (size int64, mimeType, fileName string, ok bool) {
	if msg == nil {
		return 0, "", "", false
	}
	media, ok := msg.GetMedia()
	if !ok {
		return 0, "", "", false
	}
	documentMedia, ok := media.(*tg.MessageMediaDocument)
	if !ok {
		return 0, "", "", false
	}
	documentClass, ok := documentMedia.GetDocument()
	if !ok {
		return 0, "", "", false
	}
	document, ok := documentClass.(*tg.Document)
	if !ok {
		return 0, "", "", false
	}
	name := ""
	for _, attribute := range document.GetAttributes() {
		if filename, ok := attribute.(*tg.DocumentAttributeFilename); ok {
			name = filename.FileName
			break
		}
	}
	return document.GetSize(), document.GetMimeType(), name, true
}
