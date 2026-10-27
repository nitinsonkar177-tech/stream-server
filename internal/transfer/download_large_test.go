package transfer

import (
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tgdrive/teldrive/v2/internal/db/sqlcgen"
	"testing"
)

func TestPlanSegmentsBeyondFourGiB(t *testing.T) {
	const chunk = int64(1 << 30)
	parts := make([]*sqlcgen.FilePart, 5)
	for i := range parts {
		parts[i] = &sqlcgen.FilePart{PartNo: int32(i + 1), ChannelID: 42, MessageID: int64(i + 1), PlainSize: pgtype.Int8{Int64: chunk, Valid: true}, StoredSize: pgtype.Int8{Int64: chunk, Valid: true}}
	}
	offset := int64(4<<30) - 32
	got, n, err := planSegments(parts, 5*chunk, offset, 65536)
	if err != nil || n != 65536 || len(got) != 2 {
		t.Fatalf("segments=%v length=%d err=%v", got, n, err)
	}
	if got[0].part.PartNo != 4 || got[0].offset != chunk-32 || got[0].length != 32 || got[1].part.PartNo != 5 || got[1].offset != 0 || got[1].length != 65504 {
		t.Fatal("large offset truncated or boundary mishandled")
	}
}
