package imports

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/tgdrive/teldrive/v2/internal/principal"
)

type fakeAuthenticator struct {
	identity principal.Identity
	err      error
}

func (f fakeAuthenticator) AuthenticateBearer(context.Context, string) (principal.Identity, error) {
	return f.identity, f.err
}

type fakeResolver struct {
	document *ResolvedDocument
	err      error
}

func (f fakeResolver) ResolveDocument(context.Context, int64, int64, string, int64) (*ResolvedDocument, error) {
	return f.document, f.err
}

func adminIdentity() principal.Identity {
	return principal.Identity{UserID: 7, SessionID: uuid.New(), Roles: []string{"owner"}}
}

func newTestHandler(auth Authenticator, resolver Resolver) http.Handler {
	return NewHTTPHandler(auth, &Service{resolver: resolver})
}

func postLink(t *testing.T, handler http.Handler, body string, withCookie bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/import/telegram-link", strings.NewReader(body))
	if withCookie {
		req.AddCookie(&http.Cookie{Name: accessCookieName, Value: "session-token"})
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func problemCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("response is not a problem payload: %v (%q)", err, recorder.Body.String())
	}
	return payload.Error.Code
}

func TestHandlerRejectsNonPost(t *testing.T) {
	handler := newTestHandler(fakeAuthenticator{identity: adminIdentity()}, fakeResolver{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/import/telegram-link", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", recorder.Code)
	}
}

func TestHandlerRequiresSessionCookie(t *testing.T) {
	handler := newTestHandler(fakeAuthenticator{identity: adminIdentity()}, fakeResolver{})
	recorder := postLink(t, handler, `{"url":"https://t.me/channel/1"}`, false)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}

func TestHandlerRejectsInvalidSession(t *testing.T) {
	handler := newTestHandler(fakeAuthenticator{err: errors.New("bad token")}, fakeResolver{})
	recorder := postLink(t, handler, `{"url":"https://t.me/channel/1"}`, true)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}

func TestHandlerRequiresAdminRole(t *testing.T) {
	member := principal.Identity{UserID: 9, SessionID: uuid.New(), Roles: []string{"member"}}
	handler := newTestHandler(fakeAuthenticator{identity: member}, fakeResolver{})
	recorder := postLink(t, handler, `{"url":"https://t.me/channel/1"}`, true)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
}

func TestHandlerRejectsMalformedBody(t *testing.T) {
	handler := newTestHandler(fakeAuthenticator{identity: adminIdentity()}, fakeResolver{})
	recorder := postLink(t, handler, `not json`, true)
	if recorder.Code != http.StatusUnprocessableEntity || problemCode(t, recorder) != "invalid_request" {
		t.Fatalf("status/code = %d/%s", recorder.Code, problemCode(t, recorder))
	}
}

func TestHandlerMapsInvalidLink(t *testing.T) {
	handler := newTestHandler(fakeAuthenticator{identity: adminIdentity()}, fakeResolver{})
	recorder := postLink(t, handler, `{"url":"https://example.com/not-telegram"}`, true)
	if recorder.Code != http.StatusUnprocessableEntity || problemCode(t, recorder) != "invalid_telegram_link" {
		t.Fatalf("status/code = %d/%s", recorder.Code, problemCode(t, recorder))
	}
}

func TestHandlerMapsAlreadyImported(t *testing.T) {
	handler := newTestHandler(fakeAuthenticator{identity: adminIdentity()}, fakeResolver{err: ErrAlreadyImported})
	recorder := postLink(t, handler, `{"url":"https://t.me/channel/1"}`, true)
	if recorder.Code != http.StatusConflict || problemCode(t, recorder) != "telegram_link_already_imported" {
		t.Fatalf("status/code = %d/%s", recorder.Code, problemCode(t, recorder))
	}
}

func TestHandlerMapsResolverErrors(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{ErrChannelNotAccessible, http.StatusUnprocessableEntity, "telegram_channel_not_accessible"},
		{ErrNotAChannel, http.StatusUnprocessableEntity, "telegram_link_not_channel"},
		{ErrMediaNotFound, http.StatusUnprocessableEntity, "telegram_media_not_found"},
		{ErrProtectedContent, http.StatusUnprocessableEntity, "telegram_protected_content"},
		{errors.New("boom"), http.StatusInternalServerError, "import_failed"},
	}
	for _, tc := range cases {
		handler := newTestHandler(fakeAuthenticator{identity: adminIdentity()}, fakeResolver{err: tc.err})
		recorder := postLink(t, handler, `{"url":"https://t.me/channel/1"}`, true)
		if recorder.Code != tc.status || problemCode(t, recorder) != tc.code {
			t.Fatalf("err %v: status/code = %d/%s, want %d/%s", tc.err, recorder.Code, problemCode(t, recorder), tc.status, tc.code)
		}
	}
}
