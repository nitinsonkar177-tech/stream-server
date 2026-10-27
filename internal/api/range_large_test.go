package api

import (
	"github.com/tgdrive/teldrive/v2/internal/api/gen"
	"testing"
)

func TestHTTPRangeBeyondFourGiB(t *testing.T) {
	const size = int64(5 << 30)
	for _, tt := range []struct {
		raw            string
		offset, length int64
	}{{"bytes=4294967300-4295032835", 4294967300, 65536}, {"bytes=-65536", size - 65536, 65536}, {"bytes=4294967296-", 4294967296, 1 << 30}} {
		r, partial, err := parseRange(gen.NewOptString(tt.raw), size)
		if err != nil || !partial || r.Offset != tt.offset || r.Length != tt.length {
			t.Fatalf("%s => %+v partial=%v err=%v", tt.raw, r, partial, err)
		}
	}
}
