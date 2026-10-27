package imports

import "testing"

func TestCandidateName(t *testing.T) {
	cases := []struct {
		base    string
		attempt int
		want    string
	}{
		{"video.mp4", 0, "video.mp4"},
		{"video.mp4", 1, "video (2).mp4"},
		{"video.mp4", 9, "video (10).mp4"},
		{"noext", 2, "noext (3)"},
		{"archive.tar.gz", 1, "archive.tar (2).gz"},
	}
	for _, tc := range cases {
		if got := candidateName(tc.base, tc.attempt); got != tc.want {
			t.Fatalf("candidateName(%q, %d) = %q, want %q", tc.base, tc.attempt, got, tc.want)
		}
	}
}

func TestSanitizeFileName(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		messageID int64
		mimeType  string
		want      string
	}{
		{"keeps clean name", "My Movie 2024.mp4", 7, "video/mp4", "My Movie 2024.mp4"},
		{"strips path separators", "../evil/movie.mp4", 7, "video/mp4", "evil_movie.mp4"},
		{"strips control chars", "bad\x00name.mp4", 7, "video/mp4", "bad_name.mp4"},
		{"empty falls back to mp4", "", 42, "video/mp4", "telegram-video-42.mp4"},
		{"empty falls back to mkv", "  ", 42, "video/x-matroska", "telegram-video-42.mkv"},
		{"empty unknown mime", "", 42, "application/x-bittorrent", "telegram-video-42.bin"},
		{"dot-only name", "...", 42, "video/webm", "telegram-video-42.webm"},
		{"overlong name falls back", string(make([]byte, 300)), 42, "video/mp4", "telegram-video-42.mp4"},
	}
	for _, tc := range cases {
		if got := sanitizeFileName(tc.raw, tc.messageID, tc.mimeType); got != tc.want {
			t.Fatalf("sanitizeFileName(%q, %d, %q) = %q, want %q", tc.raw, tc.messageID, tc.mimeType, got, tc.want)
		}
	}
}
