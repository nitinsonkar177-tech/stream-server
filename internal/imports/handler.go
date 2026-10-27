package imports

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/tgdrive/teldrive/v2/internal/principal"
)

const accessCookieName = "teldrive_access"

var (
	// ErrChannelNotAccessible marks links whose channel cannot be resolved
	// with the importing user's own Telegram session.
	ErrChannelNotAccessible = errors.New("telegram channel not accessible with the owner's session")
	// ErrNotAChannel marks links that resolve to a user or bot, not a channel.
	ErrNotAChannel = errors.New("telegram link does not point to a channel")
	// ErrMediaNotFound marks links whose message carries no media document.
	ErrMediaNotFound = errors.New("telegram message has no media document")
	// ErrProtectedContent marks messages or channels flagged as protected or
	// paid; they are rejected explicitly.
	ErrProtectedContent = errors.New("telegram message is protected content")
)

// Authenticator validates the browser session cookie value. It matches the
// api package's authenticator contract.
type Authenticator interface {
	AuthenticateBearer(context.Context, string) (principal.Identity, error)
}

type importLinkRequest struct {
	URL string `json:"url"`
}

type importLinkResponse struct {
	FileID   string `json:"fileId"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	MIMEType string `json:"mimeType"`
}

// NewHTTPHandler returns a plain HTTP handler for POST
// /api/v1/import/telegram-link. It authenticates the browser session cookie
// itself because it is mounted outside the generated API server.
func NewHTTPHandler(authenticator Authenticator, service *Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only POST is supported.")
			return
		}
		cookie, err := r.Cookie(accessCookieName)
		if err != nil || strings.TrimSpace(cookie.Value) == "" {
			writeProblem(w, http.StatusUnauthorized, "unauthenticated", "Sign in to import Telegram links.")
			return
		}
		identity, err := authenticator.AuthenticateBearer(r.Context(), cookie.Value)
		if err != nil || identity.UserID <= 0 {
			writeProblem(w, http.StatusUnauthorized, "unauthenticated", "Sign in to import Telegram links.")
			return
		}
		if !hasAdminRole(identity.Roles) {
			writeProblem(w, http.StatusForbidden, "forbidden", "Telegram link imports require administrator access.")
			return
		}
		var request importLinkRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		if err := decoder.Decode(&request); err != nil {
			writeProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Request body must be JSON with a url field.")
			return
		}
		result, err := service.ImportTelegramLink(r.Context(), identity.UserID, request.URL)
		if err != nil {
			writeImportError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(importLinkResponse{
			FileID:   result.FileID.String(),
			Name:     result.Name,
			Size:     result.Size,
			MIMEType: result.MIMEType,
		})
	})
}

func hasAdminRole(roles []string) bool {
	for _, role := range roles {
		if role == "admin" || role == "owner" {
			return true
		}
	}
	return false
}

func writeImportError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidLink):
		writeProblem(w, http.StatusUnprocessableEntity, "invalid_telegram_link", "Paste a Telegram message link like t.me/channel/123.")
	case errors.Is(err, ErrUnsupportedTarget):
		writeProblem(w, http.StatusUnprocessableEntity, "unsupported_telegram_link", "Only channel message links are supported, not invite or share links.")
	case errors.Is(err, ErrAlreadyImported):
		writeProblem(w, http.StatusConflict, "telegram_link_already_imported", "This Telegram message is already in your drive.")
	case errors.Is(err, ErrChannelNotAccessible):
		writeProblem(w, http.StatusUnprocessableEntity, "telegram_channel_not_accessible", "Your Telegram account cannot access this channel.")
	case errors.Is(err, ErrNotAChannel):
		writeProblem(w, http.StatusUnprocessableEntity, "telegram_link_not_channel", "This link points to a user or bot, not a channel.")
	case errors.Is(err, ErrMediaNotFound):
		writeProblem(w, http.StatusUnprocessableEntity, "telegram_media_not_found", "No media found at this Telegram message link.")
	case errors.Is(err, ErrProtectedContent):
		writeProblem(w, http.StatusUnprocessableEntity, "telegram_protected_content", "This Telegram message is protected or paid content and cannot be imported.")
	default:
		writeProblem(w, http.StatusInternalServerError, "import_failed", "The Telegram link import failed.")
	}
}

func writeProblem(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}
