// Package maintenance holds operator-triggered, one-off upkeep actions that
// run on the server itself. The revoke-session action terminates exactly one
// stored Telegram authorization (auth.logOut) and erases its stored key
// material, without touching any other session. It exists so the specific
// 2026-10-31 test login can be cleaned up without a broad logout semantic
// change and without copying session material off the server.
package maintenance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tgerr"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tgdrive/teldrive/v2/internal/db/sqlcgen"
	"github.com/tgdrive/teldrive/v2/internal/secureblob"
	"github.com/tgdrive/teldrive/v2/internal/telegramstore"
	"github.com/tgdrive/teldrive/v2/internal/telethonsession"
)

// RevokeSessionEnvVar gates the one-off revocation. It must be set to
// exactly expectedTestSessionID; any other value is refused. The variable is
// removed again after the cleanup completes.
const RevokeSessionEnvVar = "TELDRIVE_CLEANUP_REVOKE_SESSION_ID"

// Identity guards for the one session this cleanup may ever touch: the
// 2026-10-31 20:42 IST test login of the owner account. The guards fail
// closed so the action can never become a generic session-revocation route.
const (
	expectedTestSessionID = "c4d72f0a-5c5c-4c70-9fab-6be8070ef5db"
	expectedSessionOwner  = int64(7191721922)
)

var (
	testSessionCreatedNotBefore = time.Date(2026, 10, 31, 15, 0, 0, 0, time.UTC)
	testSessionCreatedNotAfter  = time.Date(2026, 10, 31, 16, 0, 0, 0, time.UTC)
)

// RevokeSessionReport describes exactly what happened to the targeted
// session so the cleanup can be verified from logs.
type RevokeSessionReport struct {
	Requested      string `json:"requested"`
	Found          bool   `json:"found"`
	HadKeyMaterial bool   `json:"had_key_material"`
	LoggedOut      bool   `json:"logged_out"`
	AlreadyInvalid bool   `json:"already_invalid"`
	KeyCleared     bool   `json:"key_cleared"`
}

// SessionKeyRecord is the identity and key material of one session row.
type SessionKeyRecord struct {
	UserID    int64
	Blob      []byte
	Revoked   bool
	CreatedAt time.Time
	Found     bool
}

// SessionKeyStore reads and erases the stored Telegram key material of one
// session row, addressed by its exact session ID.
type SessionKeyStore interface {
	LoadKeyMaterial(ctx context.Context, sessionID string) (SessionKeyRecord, error)
	ClearKeyMaterial(ctx context.Context, sessionID string) error
}

// LogoutClient is the slice of a Telegram client the cleanup needs.
type LogoutClient interface {
	Run(ctx context.Context, f func(ctx context.Context) error) error
	AuthLogOut(ctx context.Context) error
}

// refusalReason enforces the identity guards. It returns "" only when the
// record is exactly the guarded test session.
func refusalReason(rec SessionKeyRecord) string {
	if !rec.Found {
		return "session row not found"
	}
	if rec.UserID != expectedSessionOwner {
		return "session owner does not match the guarded owner"
	}
	if !rec.Revoked {
		return "session is not app-side revoked; refusing to touch a live session"
	}
	if rec.CreatedAt.Before(testSessionCreatedNotBefore) || rec.CreatedAt.After(testSessionCreatedNotAfter) {
		return "session creation time is outside the guarded test window"
	}
	return ""
}

// RevokeStoredTelegramSession terminates the Telegram authorization behind
// exactly the guarded test session and then erases that session's stored key
// material. Only the row identified by sessionID is read or modified. A
// session whose key Telegram already forgot is still cleaned locally. Any
// guard failure or Telegram error leaves the stored material untouched.
func RevokeStoredTelegramSession(
	ctx context.Context,
	store SessionKeyStore,
	newClient func(storage telegram.SessionStorage, accountID int64) (LogoutClient, error),
	decode func(ctx context.Context, blob []byte) ([]byte, error),
	logger *slog.Logger,
	sessionID string,
) (*RevokeSessionReport, error) {
	report := &RevokeSessionReport{Requested: sessionID}
	if logger == nil {
		logger = slog.Default()
	}
	if sessionID != expectedTestSessionID {
		logger.Info("maintenance.revoke_session.refused", "reason", "session id is not the guarded test session")
		return report, fmt.Errorf("maintenance: refusing session %q: not the guarded test session", sessionID)
	}
	if store == nil || newClient == nil || decode == nil {
		return report, errors.New("maintenance: revoke session dependencies are incomplete")
	}
	rec, err := store.LoadKeyMaterial(ctx, sessionID)
	if err != nil {
		return report, fmt.Errorf("load session %s key material: %w", sessionID, err)
	}
	report.Found = rec.Found
	if reason := refusalReason(rec); reason != "" {
		logger.Info("maintenance.revoke_session.refused", "reason", reason)
		return report, fmt.Errorf("maintenance: refusing session %s: %s", sessionID, reason)
	}
	if len(rec.Blob) == 0 {
		logger.Info("maintenance.revoke_session.skipped", "reason", "no stored key material", "session_id", sessionID)
		return report, nil
	}
	report.HadKeyMaterial = true
	raw, err := decode(ctx, rec.Blob)
	if err != nil {
		return report, fmt.Errorf("decode session %s key material: %w", sessionID, err)
	}
	// Fresh client bound ONLY to this session's own key material; it never
	// falls back to any cached or latest-active owner session.
	client, err := newClient(staticSessionStorage{raw: raw}, rec.UserID)
	if err != nil {
		return report, fmt.Errorf("build Telegram client for session %s: %w", sessionID, err)
	}
	runErr := client.Run(ctx, func(runCtx context.Context) error {
		return client.AuthLogOut(runCtx)
	})
	switch {
	case runErr == nil:
		report.LoggedOut = true
	case tgerr.Is(runErr, "AUTH_KEY_UNREGISTERED"):
		// Telegram already forgot this authorization; the local material
		// is dead either way, so the local cleanup still proceeds.
		report.AlreadyInvalid = true
	default:
		// Telegram failed: preserve the stored material and report.
		return report, fmt.Errorf("terminate Telegram authorization for session %s: %w", sessionID, runErr)
	}
	if err := store.ClearKeyMaterial(ctx, sessionID); err != nil {
		return report, fmt.Errorf("erase session %s key material: %w", sessionID, err)
	}
	report.KeyCleared = true
	logger.Info("maintenance.revoke_session.completed",
		"session_id", sessionID,
		"logged_out", report.LoggedOut,
		"already_invalid", report.AlreadyInvalid,
		"key_cleared", report.KeyCleared,
	)
	return report, nil
}

// RevokeSessionFromEnv runs the one-off revocation when RevokeSessionEnvVar
// is set. It returns (nil, nil) when the variable is unset or empty, and
// refuses any value other than the guarded test session UUID.
func RevokeSessionFromEnv(ctx context.Context, pool *pgxpool.Pool, cipher *secureblob.Cipher, factory *telegramstore.Factory, logger *slog.Logger) (*RevokeSessionReport, error) {
	raw := strings.TrimSpace(os.Getenv(RevokeSessionEnvVar))
	if raw == "" {
		return nil, nil
	}
	if _, err := uuid.Parse(raw); err != nil {
		return nil, fmt.Errorf("maintenance: %s must be a session UUID: %w", RevokeSessionEnvVar, err)
	}
	if raw != expectedTestSessionID {
		return nil, fmt.Errorf("maintenance: refusing %s value: not the guarded test session", RevokeSessionEnvVar)
	}
	if pool == nil || cipher == nil || factory == nil {
		return nil, errors.New("maintenance: revoke session requires database, cipher, and remote Telegram factory")
	}
	return RevokeStoredTelegramSession(ctx,
		pgSessionKeyStore{pool: pool},
		factoryLogoutClient(factory),
		cipherSessionDecoder(cipher),
		logger,
		raw,
	)
}

// staticSessionStorage hands gotd the exact decoded session bytes and
// silently drops any session update, because the cleanup erases the material
// itself afterwards.
type staticSessionStorage struct{ raw []byte }

func (s staticSessionStorage) LoadSession(context.Context) ([]byte, error) { return s.raw, nil }
func (s staticSessionStorage) StoreSession(context.Context, []byte) error  { return nil }

type pgSessionKeyStore struct{ pool *pgxpool.Pool }

func (s pgSessionKeyStore) LoadKeyMaterial(ctx context.Context, sessionID string) (SessionKeyRecord, error) {
	var rec SessionKeyRecord
	err := s.pool.QueryRow(ctx,
		"SELECT user_id, telegram_session, (revoked_at IS NOT NULL), created_at FROM "+
			sqlcgen.QualifiedName("sessions")+" WHERE id = $1",
		sessionID,
	).Scan(&rec.UserID, &rec.Blob, &rec.Revoked, &rec.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionKeyRecord{}, nil
	}
	if err != nil {
		return SessionKeyRecord{}, err
	}
	rec.Found = true
	return rec, nil
}

func (s pgSessionKeyStore) ClearKeyMaterial(ctx context.Context, sessionID string) error {
	tag, err := s.pool.Exec(ctx,
		"UPDATE "+sqlcgen.QualifiedName("sessions")+" SET telegram_session = $2 WHERE id = $1",
		sessionID, []byte{},
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("expected exactly one session row, updated %d", tag.RowsAffected())
	}
	return nil
}

type gotdLogoutClient struct{ client *telegram.Client }

func (c gotdLogoutClient) Run(ctx context.Context, f func(ctx context.Context) error) error {
	return c.client.Run(ctx, f)
}

func (c gotdLogoutClient) AuthLogOut(ctx context.Context) error {
	_, err := c.client.API().AuthLogOut(ctx)
	return err
}

func factoryLogoutClient(factory *telegramstore.Factory) func(telegram.SessionStorage, int64) (LogoutClient, error) {
	return func(storage telegram.SessionStorage, accountID int64) (LogoutClient, error) {
		client, err := factory.NewForAccount(storage, accountID, nil)
		if err != nil {
			return nil, err
		}
		return gotdLogoutClient{client: client}, nil
	}
}

func cipherSessionDecoder(cipher *secureblob.Cipher) func(context.Context, []byte) ([]byte, error) {
	return func(ctx context.Context, blob []byte) ([]byte, error) {
		plain, err := cipher.Open("telegram-session", blob)
		if err != nil {
			return nil, fmt.Errorf("decrypt Telegram session: %w", err)
		}
		raw, err := telethonsession.DecodeToGotd(ctx, string(plain))
		if err != nil {
			return nil, fmt.Errorf("load Telethon Telegram session: %w", err)
		}
		return raw, nil
	}
}
