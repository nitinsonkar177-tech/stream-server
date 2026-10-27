package imports

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tgdrive/teldrive/v2/internal/db/sqlcgen"
)

var (
	// ErrAlreadyImported marks a Telegram message that is already registered
	// as a catalog part.
	ErrAlreadyImported = errors.New("telegram message already imported")
	// ErrNoResolver marks a deployment whose Telegram storage cannot resolve
	// message links.
	ErrNoResolver = errors.New("telegram link resolving is unavailable")
)

// ResolvedDocument is the media metadata of a Telegram channel message,
// resolved through the importing user's own Telegram session.
type ResolvedDocument struct {
	ChannelID int64
	MessageID int64
	Size      int64
	MIMEType  string
	FileName  string
}

// Resolver resolves a Telegram channel message link to its media metadata
// without downloading media bytes. Production wiring adapts
// telegramstore.LinkResolver to this interface.
type Resolver interface {
	ResolveDocument(ctx context.Context, userID, channelID int64, username string, messageID int64) (*ResolvedDocument, error)
}

// Result describes a file created by a reference import.
type Result struct {
	FileID   uuid.UUID
	Name     string
	Size     int64
	MIMEType string
}

// Service performs reference imports of Telegram message media.
type Service struct {
	pool     *pgxpool.Pool
	resolver Resolver
}

// NewService validates dependencies and returns the import service.
func NewService(pool *pgxpool.Pool, resolver Resolver) (*Service, error) {
	if pool == nil || resolver == nil {
		return nil, errors.New("imports service requires a database pool and resolver")
	}
	return &Service{pool: pool, resolver: resolver}, nil
}

// ImportTelegramLink registers a Telegram channel message's media as a file
// at the importing user's drive root. No media bytes are copied: the catalog
// rows reference the existing Telegram message, and playback fetches through
// the owner's own Telegram session exactly like an uploaded part.
func (s *Service) ImportTelegramLink(ctx context.Context, userID int64, rawURL string) (*Result, error) {
	if userID <= 0 {
		return nil, errors.New("imports require an authenticated user")
	}
	ref, err := ParseTelegramLink(rawURL)
	if err != nil {
		return nil, err
	}
	document, err := s.resolver.ResolveDocument(ctx, userID, ref.ChannelID, ref.Username, ref.MessageID)
	if err != nil {
		return nil, err
	}
	if document == nil || document.ChannelID <= 0 || document.MessageID <= 0 || document.Size <= 0 {
		return nil, errors.New("telegram link did not resolve to a media document")
	}
	name := sanitizeFileName(document.FileName, document.MessageID, document.MIMEType)
	mimeType := strings.TrimSpace(document.MIMEType)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin import transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var fileID pgtype.UUID
	for attempt := 0; attempt < 20; attempt++ {
		candidate := candidateName(name, attempt)
		exists, err := fileNameExistsAtRoot(ctx, tx, userID, candidate)
		if err != nil {
			return nil, err
		}
		if exists {
			continue
		}
		err = tx.QueryRow(ctx,
			`INSERT INTO `+sqlcgen.QualifiedName("files")+` (user_id, parent_id, name, kind, mime_type, size, mod_time)
			 VALUES ($1, NULL, $2, 'file', $3, $4, now())
			 RETURNING id`,
			userID, candidate, mimeType, document.Size,
		).Scan(&fileID)
		if err == nil {
			name = candidate
			break
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			continue
		}
		return nil, fmt.Errorf("create imported file: %w", err)
	}
	if !fileID.Valid {
		return nil, errors.New("could not find a free file name for the import")
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO `+sqlcgen.QualifiedName("file_parts")+` (file_id, part_no, channel_id, message_id, plain_size, stored_size)
		 VALUES ($1, 1, $2, $3, $4, $4)`,
		fileID, document.ChannelID, document.MessageID, document.Size,
	); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && strings.Contains(pgErr.ConstraintName, "channel_id") {
			return nil, ErrAlreadyImported
		}
		return nil, fmt.Errorf("register imported part: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit import: %w", err)
	}
	return &Result{FileID: fileID.Bytes, Name: name, Size: document.Size, MIMEType: mimeType}, nil
}

func fileNameExistsAtRoot(ctx context.Context, tx pgx.Tx, userID int64, name string) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx,
		`SELECT EXISTS(
			SELECT 1 FROM `+sqlcgen.QualifiedName("files")+`
			WHERE user_id = $1 AND parent_id IS NULL AND status = 'active' AND name = $2
		)`, userID, name,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check imported file name: %w", err)
	}
	return exists, nil
}

// candidateName returns base for attempt 0 and a numbered variant otherwise,
// keeping the file extension: "video.mp4" -> "video (2).mp4".
func candidateName(base string, attempt int) string {
	if attempt <= 0 {
		return base
	}
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	return fmt.Sprintf("%s (%d)%s", stem, attempt+1, ext)
}

var unsafeFileNameChars = regexp.MustCompile(`[/\\\x00-\x1f\x7f]+`)

// sanitizeFileName cleans a Telegram-provided file name and derives a
// fallback from the message id and MIME type when the name is unusable.
func sanitizeFileName(raw string, messageID int64, mimeType string) string {
	name := strings.TrimSpace(unsafeFileNameChars.ReplaceAllString(raw, "_"))
	name = strings.Trim(name, ". _")
	if name != "" && len(name) <= 200 {
		return name
	}
	ext := ".bin"
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "video/mp4":
		ext = ".mp4"
	case "video/x-matroska":
		ext = ".mkv"
	case "video/webm":
		ext = ".webm"
	case "video/quicktime":
		ext = ".mov"
	case "video/x-msvideo":
		ext = ".avi"
	}
	return fmt.Sprintf("telegram-video-%d%s", messageID, ext)
}
