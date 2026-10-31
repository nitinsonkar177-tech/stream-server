package maintenance

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tgerr"
)

var testCreatedAt = time.Date(2026, 10, 31, 15, 12, 22, 0, time.UTC)

type fakeStore struct {
	rec      SessionKeyRecord
	loadErr  error
	loadedID string

	clearedID string
	clearErr  error
	clearN    int
}

func (s *fakeStore) LoadKeyMaterial(_ context.Context, sessionID string) (SessionKeyRecord, error) {
	s.loadedID = sessionID
	return s.rec, s.loadErr
}

func (s *fakeStore) ClearKeyMaterial(_ context.Context, sessionID string) error {
	s.clearedID = sessionID
	s.clearN++
	return s.clearErr
}

type fakeClient struct {
	runCalls    int
	logoutCalls int
	logoutErr   error
	storageUsed telegram.SessionStorage
}

func (c *fakeClient) Run(ctx context.Context, f func(ctx context.Context) error) error {
	c.runCalls++
	return f(ctx)
}

func (c *fakeClient) AuthLogOut(ctx context.Context) error {
	c.logoutCalls++
	return c.logoutErr
}

type clientFactory struct {
	client    *fakeClient
	accountID int64
	calls     int
	err       error
}

func (n *clientFactory) new(storage telegram.SessionStorage, accountID int64) (LogoutClient, error) {
	n.calls++
	n.accountID = accountID
	n.client.storageUsed = storage
	if n.err != nil {
		return nil, n.err
	}
	return n.client, nil
}

func fakeDecode(called *int, err error) func(context.Context, []byte) ([]byte, error) {
	return func(context.Context, []byte) ([]byte, error) {
		*called++
		if err != nil {
			return nil, err
		}
		return []byte("decoded-session"), nil
	}
}

func guardedRecord() SessionKeyRecord {
	return SessionKeyRecord{
		UserID:    expectedSessionOwner,
		Blob:      []byte("encrypted"),
		Revoked:   true,
		CreatedAt: testCreatedAt,
		Found:     true,
	}
}

func runRevoke(t *testing.T, store *fakeStore, factory *clientFactory, decodeCalls *int, sessionID string) (*RevokeSessionReport, error) {
	t.Helper()
	return RevokeStoredTelegramSession(context.Background(), store, factory.new, fakeDecode(decodeCalls, nil), nil, sessionID)
}

func TestRevokeRefusesNonGuardedSessionID(t *testing.T) {
	store := &fakeStore{rec: guardedRecord()}
	factory := &clientFactory{client: &fakeClient{}}
	decodeCalls := 0
	_, err := runRevoke(t, store, factory, &decodeCalls, "00000000-0000-0000-0000-000000000000")
	if err == nil || !strings.Contains(err.Error(), "not the guarded test session") {
		t.Fatalf("expected refusal, got %v", err)
	}
	if store.loadedID != "" || factory.calls != 0 || store.clearN != 0 {
		t.Fatalf("refusal must not touch store or Telegram: loaded=%q factory=%d clear=%d", store.loadedID, factory.calls, store.clearN)
	}
}

func TestRevokeRefusesWrongOwner(t *testing.T) {
	rec := guardedRecord()
	rec.UserID = 1
	store := &fakeStore{rec: rec}
	factory := &clientFactory{client: &fakeClient{}}
	decodeCalls := 0
	_, err := runRevoke(t, store, factory, &decodeCalls, expectedTestSessionID)
	if err == nil || !strings.Contains(err.Error(), "owner") {
		t.Fatalf("expected owner refusal, got %v", err)
	}
	if factory.calls != 0 || store.clearN != 0 {
		t.Fatalf("refusal must not touch Telegram or clear: factory=%d clear=%d", factory.calls, store.clearN)
	}
}

func TestRevokeRefusesLiveSession(t *testing.T) {
	rec := guardedRecord()
	rec.Revoked = false
	store := &fakeStore{rec: rec}
	factory := &clientFactory{client: &fakeClient{}}
	decodeCalls := 0
	_, err := runRevoke(t, store, factory, &decodeCalls, expectedTestSessionID)
	if err == nil || !strings.Contains(err.Error(), "not app-side revoked") {
		t.Fatalf("expected live-session refusal, got %v", err)
	}
	if factory.calls != 0 || store.clearN != 0 {
		t.Fatalf("refusal must not touch Telegram or clear: factory=%d clear=%d", factory.calls, store.clearN)
	}
}

func TestRevokeRefusesOutsideWindow(t *testing.T) {
	rec := guardedRecord()
	rec.CreatedAt = time.Date(2026, 10, 28, 12, 48, 27, 0, time.UTC) // the older owner session's shape
	store := &fakeStore{rec: rec}
	factory := &clientFactory{client: &fakeClient{}}
	decodeCalls := 0
	_, err := runRevoke(t, store, factory, &decodeCalls, expectedTestSessionID)
	if err == nil || !strings.Contains(err.Error(), "outside the guarded test window") {
		t.Fatalf("expected window refusal, got %v", err)
	}
	if factory.calls != 0 || store.clearN != 0 {
		t.Fatalf("refusal must not touch Telegram or clear: factory=%d clear=%d", factory.calls, store.clearN)
	}
}

func TestRevokeSessionNotFound(t *testing.T) {
	store := &fakeStore{rec: SessionKeyRecord{Found: false}}
	factory := &clientFactory{client: &fakeClient{}}
	decodeCalls := 0
	report, err := runRevoke(t, store, factory, &decodeCalls, expectedTestSessionID)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found refusal, got report=%+v err=%v", report, err)
	}
	if factory.calls != 0 || decodeCalls != 0 || store.clearN != 0 {
		t.Fatalf("no work expected: factory=%d decode=%d clear=%d", factory.calls, decodeCalls, store.clearN)
	}
}

func TestRevokeSessionNoKeyMaterial(t *testing.T) {
	rec := guardedRecord()
	rec.Blob = nil
	store := &fakeStore{rec: rec}
	factory := &clientFactory{client: &fakeClient{}}
	decodeCalls := 0
	report, err := runRevoke(t, store, factory, &decodeCalls, expectedTestSessionID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.Found || report.HadKeyMaterial || report.LoggedOut || report.KeyCleared {
		t.Fatalf("unexpected report: %+v", report)
	}
	if factory.calls != 0 || decodeCalls != 0 || store.clearN != 0 {
		t.Fatalf("no work expected: factory=%d decode=%d clear=%d", factory.calls, decodeCalls, store.clearN)
	}
}

func TestRevokeSessionHappyPath(t *testing.T) {
	store := &fakeStore{rec: guardedRecord()}
	client := &fakeClient{}
	factory := &clientFactory{client: client}
	decodeCalls := 0
	report, err := runRevoke(t, store, factory, &decodeCalls, expectedTestSessionID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.Found || !report.HadKeyMaterial || !report.LoggedOut || report.AlreadyInvalid || !report.KeyCleared {
		t.Fatalf("unexpected report: %+v", report)
	}
	if factory.calls != 1 || factory.accountID != expectedSessionOwner {
		t.Fatalf("client not built for the row owner: calls=%d account=%d", factory.calls, factory.accountID)
	}
	if client.runCalls != 1 || client.logoutCalls != 1 {
		t.Fatalf("logout not executed exactly once: run=%d logout=%d", client.runCalls, client.logoutCalls)
	}
	if store.clearedID != expectedTestSessionID || store.clearN != 1 {
		t.Fatalf("cleared wrong session: %q count=%d", store.clearedID, store.clearN)
	}
	// The storage handed to Telegram must serve the decoded bytes and drop writes.
	raw, err := client.storageUsed.LoadSession(context.Background())
	if err != nil || string(raw) != "decoded-session" {
		t.Fatalf("storage load: raw=%q err=%v", raw, err)
	}
	if err := client.storageUsed.StoreSession(context.Background(), []byte("x")); err != nil {
		t.Fatalf("storage store must be a no-op: %v", err)
	}
	if store.clearN != 1 {
		t.Fatalf("storage writes must not reach the database: clear=%d", store.clearN)
	}
}

func TestRevokeSessionAlreadyInvalidStillClears(t *testing.T) {
	store := &fakeStore{rec: guardedRecord()}
	client := &fakeClient{logoutErr: tgerr.New(401, "AUTH_KEY_UNREGISTERED")}
	factory := &clientFactory{client: client}
	decodeCalls := 0
	report, err := runRevoke(t, store, factory, &decodeCalls, expectedTestSessionID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.LoggedOut || !report.AlreadyInvalid || !report.KeyCleared {
		t.Fatalf("unexpected report: %+v", report)
	}
	if store.clearN != 1 || store.clearedID != expectedTestSessionID {
		t.Fatalf("dead key material must still be erased: %q count=%d", store.clearedID, store.clearN)
	}
}

func TestRevokeSessionTelegramErrorKeepsMaterial(t *testing.T) {
	store := &fakeStore{rec: guardedRecord()}
	client := &fakeClient{logoutErr: tgerr.New(500, "INTERNAL")}
	factory := &clientFactory{client: client}
	decodeCalls := 0
	report, err := runRevoke(t, store, factory, &decodeCalls, expectedTestSessionID)
	if err == nil || !strings.Contains(err.Error(), expectedTestSessionID) {
		t.Fatalf("expected naming error, got %v", err)
	}
	if report.KeyCleared || store.clearN != 0 {
		t.Fatalf("key material must survive a failed logout: %+v clear=%d", report, store.clearN)
	}
}

func TestRevokeSessionClearFailureSurfaces(t *testing.T) {
	rec := guardedRecord()
	store := &fakeStore{rec: rec, clearErr: errors.New("db down")}
	factory := &clientFactory{client: &fakeClient{}}
	decodeCalls := 0
	report, err := runRevoke(t, store, factory, &decodeCalls, expectedTestSessionID)
	if err == nil {
		t.Fatal("expected clear error")
	}
	if !report.LoggedOut || report.KeyCleared {
		t.Fatalf("logout happened but clear must be reported failed: %+v", report)
	}
}
