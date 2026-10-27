package imports

import (
	"errors"
	"testing"
)

func TestParseTelegramLinkValid(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		channelID int64
		username  string
		messageID int64
	}{
		{"private channel link", "https://t.me/c/1234567890/42", 1234567890, "", 42},
		{"public username link", "https://t.me/some_channel/123", 0, "some_channel", 123},
		{"public preview link", "https://t.me/s/some_channel/123", 0, "some_channel", 123},
		{"no scheme", "t.me/some_channel/123", 0, "some_channel", 123},
		{"telegram.me host", "https://telegram.me/some_channel/123", 0, "some_channel", 123},
		{"www subdomain", "https://www.t.me/some_channel/123", 0, "some_channel", 123},
		{"query and fragment stripped", "https://t.me/some_channel/123?single#comment", 0, "some_channel", 123},
		{"whitespace trimmed", "  https://t.me/some_channel/123  ", 0, "some_channel", 123},
		{"http scheme", "http://t.me/some_channel/123", 0, "some_channel", 123},
		{"underscore username", "https://t.me/a_b_c9/7", 0, "a_b_c9", 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := ParseTelegramLink(tc.raw)
			if err != nil {
				t.Fatalf("ParseTelegramLink(%q) error: %v", tc.raw, err)
			}
			if ref.ChannelID != tc.channelID || ref.Username != tc.username || ref.MessageID != tc.messageID {
				t.Fatalf("ParseTelegramLink(%q) = %+v, want channel=%d user=%q msg=%d", tc.raw, ref, tc.channelID, tc.username, tc.messageID)
			}
		})
	}
}

func TestParseTelegramLinkInvalid(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"spaces", "   "},
		{"foreign host", "https://example.com/some_channel/123"},
		{"lookalike host", "https://t.me.evil.com/some_channel/123"},
		{"no message id", "https://t.me/some_channel"},
		{"non numeric message id", "https://t.me/some_channel/abc"},
		{"zero message id", "https://t.me/some_channel/0"},
		{"negative message id", "https://t.me/some_channel/-5"},
		{"too many segments", "https://t.me/some_channel/123/extra"},
		{"username too short", "https://t.me/abc/123"},
		{"username starts with digit", "https://t.me/1abcde/123"},
		{"c without ids", "https://t.me/c/123"},
		{"c non numeric channel", "https://t.me/c/abc/123"},
		{"ftp scheme", "ftp://t.me/some_channel/123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseTelegramLink(tc.raw); !errors.Is(err, ErrInvalidLink) {
				t.Fatalf("ParseTelegramLink(%q) error = %v, want ErrInvalidLink", tc.raw, err)
			}
		})
	}
}

func TestParseTelegramLinkUnsupported(t *testing.T) {
	cases := []string{
		"https://t.me/joinchat/AAAAAaaaa",
		"https://t.me/+AAAAAaaaa",
		"https://t.me/addstickers/pack",
		"https://t.me/share/url?url=x",
	}
	for _, raw := range cases {
		if _, err := ParseTelegramLink(raw); !errors.Is(err, ErrUnsupportedTarget) {
			t.Fatalf("ParseTelegramLink(%q) error = %v, want ErrUnsupportedTarget", raw, err)
		}
	}
}
