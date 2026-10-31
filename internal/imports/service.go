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
	pool        *pgxpool.Pool
	resolver    Resolver
	invalidator interface {
		InvalidateFiles(context.Context, int64, ...uuid.UUID)
	}
}

// NewService validates dependencies and returns the import service.
func NewService(pool *pgxpool.Pool, resolver Resolver) (*Service, error) {
	if pool == nil || resolver == nil {
		return nil, errors.New("imports service requires a database pool and resolver")
	}
	return &Service{pool: pool, resolver: resolver}, nil
}

func (s *Service) SetCacheInvalidator(i interface {
	InvalidateFiles(context.Context, int64, ...uuid.UUID)
}) {
	s.invalidator = i
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

	// Re-importing the same public link repairs only its owned reference and
	// preserves the existing file ID, name and every share URL.
	if ref.Username != "" {
		var existing Result
		var existingID pgtype.UUID
		err := tx.QueryRow(ctx, `UPDATE `+sqlcgen.QualifiedName("file_parts")+` p
 SET source_username=$1 FROM `+sqlcgen.QualifiedName("files")+` f
 WHERE p.file_id=f.id AND f.user_id=$2 AND f.status='active'
 AND p.channel_id=$3 AND p.message_id=$4
 RETURNING f.id,f.name,f.size,COALESCE(f.mime_type,'application/octet-stream')`,
			strings.ToLower(ref.Username), userID, document.ChannelID, document.MessageID).Scan(&existingID, &existing.Name, &existing.Size, &existing.MIMEType)
		if err == nil {
			existing.FileID = existingID.Bytes
			if err = tx.Commit(ctx); err != nil {
				return nil, err
			}
			if s.invalidator != nil {
				s.invalidator.InvalidateFiles(ctx, userID, existing.FileID)
			}
			return &existing, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("repair imported reference: %w", err)
		}
	}

	// A file deleted from the drive (trashed or pending permanent deletion)
	// still holds its file_parts row, and the unique (channel_id, message_id)
	// constraint then blocks a fresh import with a misleading
	// "already in your drive" conflict. The link is not in the drive any
	// more, so revive that file instead of failing: it keeps its file ID,
	// returns to the root when its folder is gone, and gets a fresh name
	// only when an active file already owns the old one.
	revived, err := reviveDeletedImport(ctx, tx, userID, document, strings.ToLower(ref.Username))
	if err != nil {
		return nil, err
	}
	if revived != nil {
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		if s.invalidator != nil {
			s.invalidator.InvalidateFiles(ctx, userID, revived.FileID)
		}
		return revived, nil
	}

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
		`INSERT INTO `+sqlcgen.QualifiedName("file_parts")+` (file_id, part_no, channel_id, message_id, plain_size, stored_size, source_username)
		 VALUES ($1, 1, $2, $3, $4, $4, NULLIF($5,''))`,
		fileID, document.ChannelID, document.MessageID, document.Size, strings.ToLower(ref.Username),
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

// reviveDeletedImport brings back the importing user's own deleted file whose
// part references the resolved Telegram message. It returns nil when no such
// deleted file exists or when a concurrent permanent deletion wins the race;
// the caller then falls through to a fresh import. Reaching this point means
// the resolver already confirmed the Telegram message and its media still
// exist, so the revived reference points at real content. Files whose folder
// is still active stay in place; others come back at the drive root, like a
// fresh import. Trashing revoked the file's shares and they stay revoked.
func reviveDeletedImport(ctx context.Context, tx pgx.Tx, userID int64, document *ResolvedDocument, username string) (*Result, error) {
	mimeType := strings.TrimSpace(document.MIMEType)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	var (
		fileID   pgtype.UUID
		name     string
		parentID pgtype.UUID
	)
	err := tx.QueryRow(ctx, `SELECT f.id, f.name, f.parent_id
 FROM `+sqlcgen.QualifiedName("file_parts")+` p
 JOIN `+sqlcgen.QualifiedName("files")+` f ON f.id = p.file_id
 WHERE f.user_id = $1 AND f.status <> 'active'
 AND p.channel_id = $2 AND p.message_id = $3
 LIMIT 1`, userID, document.ChannelID, document.MessageID).
		Scan(&fileID, &name, &parentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find deleted import: %w", err)
	}

	targetParent := parentID
	if parentID.Valid {
		var parentActive bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM `+sqlcgen.QualifiedName("files")+`
			WHERE id = $1 AND user_id = $2 AND status = 'active'
		)`, parentID, userID).Scan(&parentActive); err != nil {
			return nil, fmt.Errorf("check deleted import parent: %w", err)
		}
		if !parentActive {
			targetParent = pgtype.UUID{}
		}
	}

	finalName := name
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			finalName = candidateName(name, attempt)
		}
		exists, err := fileNameExistsAtParent(ctx, tx, userID, targetParent, finalName)
		if err != nil {
			return nil, err
		}
		if !exists {
			break
		}
		if attempt >= 19 {
			return nil, errors.New("could not find a free file name for the revived import")
		}
	}

	// One atomic conditional update closes the permanent-deletion race: the row
	// is restored only while it is still deleted AND its part row still
	// references this Telegram message. The pending-deletion purge deletes only
	// 'deletion_pending' rows, so once this commit lands the purge skips the
	// file; if the purge lands first, zero rows match and a fresh import runs.
	// Size and MIME type come from the freshly resolved document, because the
	// channel message's media may have been replaced since the first import.
	restored, err := tx.Exec(ctx, `UPDATE `+sqlcgen.QualifiedName("files")+`
 SET status = 'active', deleted_at = NULL, generation = generation + 1,
 updated_at = now(), parent_id = $1, name = $2, size = $3, mime_type = $4
 WHERE id = $5 AND user_id = $6 AND status <> 'active'
 AND EXISTS (
	SELECT 1 FROM `+sqlcgen.QualifiedName("file_parts")+` p
	WHERE p.file_id = files.id AND p.channel_id = $7 AND p.message_id = $8
 )`,
		targetParent, finalName, document.Size, mimeType,
		fileID, userID, document.ChannelID, document.MessageID)
	if err != nil {
		return nil, fmt.Errorf("revive deleted import: %w", err)
	}
	if restored.RowsAffected() == 0 {
		return nil, nil
	}
	// Refresh the stored public source exactly like the active repair path, so
	// playback of the revived file resolves the Telegram peer again.
	if _, err := tx.Exec(ctx, `UPDATE `+sqlcgen.QualifiedName("file_parts")+`
 SET source_username = NULLIF($1,''), plain_size = $2, stored_size = $2
 WHERE file_id = $3 AND channel_id = $4 AND message_id = $5`,
		username, document.Size, fileID, document.ChannelID, document.MessageID); err != nil {
		return nil, fmt.Errorf("refresh revived import source: %w", err)
	}

	return &Result{FileID: fileID.Bytes, Name: finalName, Size: document.Size, MIMEType: mimeType}, nil
}

func fileNameExistsAtRoot(ctx context.Context, tx pgx.Tx, userID int64, name string) (bool, error) {
	return fileNameExistsAtParent(ctx, tx, userID, pgtype.UUID{}, name)
}

func fileNameExistsAtParent(ctx context.Context, tx pgx.Tx, userID int64, parentID pgtype.UUID, name string) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx,
		`SELECT EXISTS(
			SELECT 1 FROM `+sqlcgen.QualifiedName("files")+`
			WHERE user_id = $1 AND parent_id IS NOT DISTINCT FROM $2 AND status = 'active' AND name = $3
		)`, userID, parentID, name,
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
