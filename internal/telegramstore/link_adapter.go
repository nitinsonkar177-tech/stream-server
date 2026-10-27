package telegramstore

import (
	"context"
	"errors"

	"github.com/tgdrive/teldrive/v2/internal/imports"
)

// ResolveDocument adapts ResolveTelegramLink to the imports.Resolver
// interface. Telegram-specific failures are translated into imports package
// sentinels so HTTP layers stay free of Telegram dependencies.
func (s *GotdStorage) ResolveDocument(ctx context.Context, userID, channelID int64, username string, messageID int64) (*imports.ResolvedDocument, error) {
	document, err := s.ResolveTelegramLink(ctx, userID, TelegramLinkRef{
		ChannelID: channelID,
		Username:  username,
		MessageID: messageID,
	})
	if err != nil {
		return nil, mapLinkError(err)
	}
	return &imports.ResolvedDocument{
		ChannelID: document.ChannelID,
		MessageID: document.MessageID,
		Size:      document.Size,
		MIMEType:  document.MIMEType,
		FileName:  document.FileName,
	}, nil
}

func mapLinkError(err error) error {
	switch {
	case errors.Is(err, ErrLinkChannelNotFound):
		return imports.ErrChannelNotAccessible
	case errors.Is(err, ErrLinkTargetNotChannel):
		return imports.ErrNotAChannel
	case errors.Is(err, ErrProtectedContent):
		return imports.ErrProtectedContent
	case errors.Is(err, ErrMessageNotFound), errors.Is(err, ErrDocumentNotFound):
		return imports.ErrMediaNotFound
	default:
		return err
	}
}
