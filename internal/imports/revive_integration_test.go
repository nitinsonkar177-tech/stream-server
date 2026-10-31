//go:build integration

package imports_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tgdrive/teldrive/v2/internal/imports"
	testpostgres "github.com/tgdrive/teldrive/v2/internal/testutil/postgres"
)

type staticResolver struct {
	document *imports.ResolvedDocument
}

func (r staticResolver) ResolveDocument(context.Context, int64, int64, string, int64) (*imports.ResolvedDocument, error) {
	return r.document, nil
}

func seedUser(t *testing.T, pool *pgxpool.Pool, userID int64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `INSERT INTO users (user_id) VALUES ($1)`, userID); err != nil {
		t.Fatal(err)
	}
}

func seedFile(t *testing.T, pool *pgxpool.Pool, userID int64, name string, status string, parent *string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(), `
INSERT INTO files (user_id, parent_id, name, kind, mime_type, size, mod_time, status, deleted_at)
VALUES ($1, $2, $3, 'file', 'video/mp4', 10, now(), $4::file_status,
        CASE WHEN $4::text = 'active' THEN NULL ELSE now() END)
RETURNING id::text`, userID, parent, name, status).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func seedPart(t *testing.T, pool *pgxpool.Pool, fileID string, channelID, messageID int64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
INSERT INTO file_parts (file_id, part_no, channel_id, message_id, plain_size, stored_size)
VALUES ($1, 1, $2, $3, 10, 10)`, fileID, channelID, messageID); err != nil {
		t.Fatal(err)
	}
}

func fileState(t *testing.T, pool *pgxpool.Pool, id string) (status, name string, parentNil bool) {
	t.Helper()
	err := pool.QueryRow(context.Background(),
		`SELECT status::text, name, parent_id IS NULL FROM files WHERE id = $1`, id).
		Scan(&status, &name, &parentNil)
	if err != nil {
		t.Fatal(err)
	}
	return status, name, parentNil
}

func testDocument() *imports.ResolvedDocument {
	return &imports.ResolvedDocument{ChannelID: 100, MessageID: 5, Size: 10, MIMEType: "video/mp4", FileName: "movie.mp4"}
}

func newImportService(t *testing.T, db *testpostgres.Database) *imports.Service {
	t.Helper()
	service, err := imports.NewService(db.Pool, staticResolver{document: testDocument()})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func importLink(t *testing.T, service *imports.Service, userID int64) *imports.Result {
	t.Helper()
	result, err := service.ImportTelegramLink(context.Background(), userID, "https://t.me/somechannel/5")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	return result
}

func TestImportRevivesTrashedFile(t *testing.T) {
	db := testpostgres.New(t)
	seedUser(t, db.Pool, 1001)
	trashedID := seedFile(t, db.Pool, 1001, "movie.mp4", "trashed", nil)
	seedPart(t, db.Pool, trashedID, 100, 5)

	service := newImportService(t, db)
	result := importLink(t, service, 1001)

	if result.FileID.String() != trashedID {
		t.Fatalf("revived import got new file ID %s, want preserved %s", result.FileID, trashedID)
	}
	if result.Name != "movie.mp4" {
		t.Fatalf("revived name = %q", result.Name)
	}
	status, _, parentNil := fileState(t, db.Pool, trashedID)
	if status != "active" || !parentNil {
		t.Fatalf("revived file state = %s parentNil=%v", status, parentNil)
	}
	var username *string
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT source_username FROM file_parts WHERE file_id = $1`, trashedID).Scan(&username); err != nil {
		t.Fatal(err)
	}
	if username == nil || *username != "somechannel" {
		t.Fatalf("source username after revive = %v", username)
	}
}

func TestImportRevivesDeletionPendingFile(t *testing.T) {
	db := testpostgres.New(t)
	seedUser(t, db.Pool, 1001)
	pendingID := seedFile(t, db.Pool, 1001, "movie.mp4", "deletion_pending", nil)
	seedPart(t, db.Pool, pendingID, 100, 5)

	result := importLink(t, newImportService(t, db), 1001)
	if result.FileID.String() != pendingID {
		t.Fatalf("deletion-pending import got new ID %s, want %s", result.FileID, pendingID)
	}
	if status, _, _ := fileState(t, db.Pool, pendingID); status != "active" {
		t.Fatalf("deletion-pending file status = %s", status)
	}
}

func TestImportReviveRenamesOnActiveNameConflict(t *testing.T) {
	db := testpostgres.New(t)
	seedUser(t, db.Pool, 1001)
	seedFile(t, db.Pool, 1001, "movie.mp4", "active", nil)
	trashedID := seedFile(t, db.Pool, 1001, "movie.mp4", "trashed", nil)
	seedPart(t, db.Pool, trashedID, 100, 5)

	result := importLink(t, newImportService(t, db), 1001)
	if result.FileID.String() != trashedID || result.Name != "movie (2).mp4" {
		t.Fatalf("conflict revive = %s %q", result.FileID, result.Name)
	}
	if status, name, _ := fileState(t, db.Pool, trashedID); status != "active" || name != "movie (2).mp4" {
		t.Fatalf("conflict revived state = %s %q", status, name)
	}
}

func TestImportReviveDetachesFromTrashedFolder(t *testing.T) {
	db := testpostgres.New(t)
	seedUser(t, db.Pool, 1001)
	var folderID string
	if err := db.Pool.QueryRow(context.Background(), `
INSERT INTO files (user_id, parent_id, name, kind, size, mod_time, status, deleted_at)
VALUES (1001, NULL, 'stuff', 'folder', 0, now(), 'trashed', now()) RETURNING id::text`).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	trashedID := seedFile(t, db.Pool, 1001, "movie.mp4", "trashed", &folderID)
	seedPart(t, db.Pool, trashedID, 100, 5)

	result := importLink(t, newImportService(t, db), 1001)
	if result.FileID.String() != trashedID {
		t.Fatalf("folder revive got new ID %s", result.FileID)
	}
	if status, _, parentNil := fileState(t, db.Pool, trashedID); status != "active" || !parentNil {
		t.Fatalf("revived out of trashed folder = %s parentNil=%v", status, parentNil)
	}
}

func TestImportReviveStaysInsideActiveFolder(t *testing.T) {
	db := testpostgres.New(t)
	seedUser(t, db.Pool, 1001)
	var folderID string
	if err := db.Pool.QueryRow(context.Background(), `
INSERT INTO files (user_id, parent_id, name, kind, size, mod_time)
VALUES (1001, NULL, 'keep', 'folder', 0, now()) RETURNING id::text`).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	trashedID := seedFile(t, db.Pool, 1001, "movie.mp4", "trashed", &folderID)
	seedPart(t, db.Pool, trashedID, 100, 5)

	result := importLink(t, newImportService(t, db), 1001)
	if result.FileID.String() != trashedID {
		t.Fatalf("active folder revive got new ID %s", result.FileID)
	}
	if status, _, parentNil := fileState(t, db.Pool, trashedID); status != "active" || parentNil {
		t.Fatalf("revived inside active folder = %s parentNil=%v", status, parentNil)
	}
}

func TestImportConflictStillReportedForActiveFile(t *testing.T) {
	db := testpostgres.New(t)
	seedUser(t, db.Pool, 1001)
	activeID := seedFile(t, db.Pool, 1001, "movie.mp4", "active", nil)
	seedPart(t, db.Pool, activeID, 100, 5)

	result := importLink(t, newImportService(t, db), 1001)
	if result.FileID.String() != activeID {
		t.Fatalf("active re-import repaired to %s, want %s", result.FileID, activeID)
	}
	var count int
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM files WHERE user_id = 1001 AND status = 'active'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("active files after repair = %d, %v", count, err)
	}
}
