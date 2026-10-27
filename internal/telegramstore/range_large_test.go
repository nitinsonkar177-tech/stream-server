package telegramstore

import "testing"

func TestTelegramRangeBeyondFourGiB(t *testing.T) {
	offset := int64(4<<30) + 12345
	plans := planTelegramReads(offset, 2*telegramReadChunk, 32)
	var n int64
	for _, p := range plans {
		if p.offset < int64(4<<30) || p.offset%1024 != 0 || p.limit%1024 != 0 || p.limit > telegramReadChunk || p.offset/telegramReadChunk != (p.offset+int64(p.limit)-1)/telegramReadChunk {
			t.Fatalf("invalid large Telegram read %+v", p)
		}
		if p.offset+int64(p.skip) != offset+n {
			t.Fatal("large-offset gap/overlap")
		}
		n += int64(p.length)
	}
	if n != 2*telegramReadChunk {
		t.Fatalf("length=%d", n)
	}
}
