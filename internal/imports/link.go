// Package imports implements reference imports of Telegram message media into
// the TelDrive catalog. A reference import registers catalog rows that point
// at an existing Telegram channel message (channel_id + message_id) without
// copying media bytes. All Telegram access happens through the importing
// user's own already-stored Telegram session, server-side only.
package imports

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var (
	// ErrInvalidLink marks input that is not a supported Telegram message link.
	ErrInvalidLink = errors.New("invalid telegram message link")
	// ErrUnsupportedTarget marks links that are Telegram links but not
	// channel-message links (invite links, sticker packs, share links, ...).
	ErrUnsupportedTarget = errors.New("unsupported telegram link target")
)

// LinkRef is the parsed form of a Telegram message link. Exactly one of
// ChannelID or Username is set.
type LinkRef struct {
	ChannelID int64
	Username  string
	MessageID int64
}

var telegramUsernamePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{3,31}$`)

// reservedPathSegments are t.me path prefixes that never denote a public
// channel username in a message link.
var reservedPathSegments = map[string]bool{
	"c": true, "s": true, "joinchat": true, "share": true,
	"addstickers": true, "addemoji": true, "proxy": true,
	"socks": true, "passport": true, "login": true, "confirmphone": true,
	"setlanguage": true, "iv": true, "wallet": true, "blog": true,
}

// ParseTelegramLink parses a Telegram message link of the forms
//
//	https://t.me/c/<channel_id>/<message_id>      (private channel)
//	https://t.me/<username>/<message_id>          (public channel)
//	https://t.me/s/<username>/<message_id>        (public web preview)
//
// Hosts t.me and telegram.me are accepted with any subdomain. Query strings
// and fragments (e.g. "?single") are ignored.
func ParseTelegramLink(raw string) (LinkRef, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return LinkRef{}, fmt.Errorf("%w: empty link", ErrInvalidLink)
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return LinkRef{}, fmt.Errorf("%w: %v", ErrInvalidLink, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return LinkRef{}, fmt.Errorf("%w: scheme %q", ErrInvalidLink, u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	if host != "t.me" && host != "telegram.me" && !strings.HasSuffix(host, ".t.me") && !strings.HasSuffix(host, ".telegram.me") {
		return LinkRef{}, fmt.Errorf("%w: host %q", ErrInvalidLink, host)
	}
	segments := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	first := segments[0]
	if strings.HasPrefix(first, "+") || first == "joinchat" {
		return LinkRef{}, fmt.Errorf("%w: invite links are not channel message links", ErrUnsupportedTarget)
	}
	if first == "c" {
		if len(segments) != 3 {
			return LinkRef{}, fmt.Errorf("%w: expected t.me/c/<channel-id>/<message-id>", ErrInvalidLink)
		}
		channelID, err := positiveID(segments[1], "channel id")
		if err != nil {
			return LinkRef{}, err
		}
		messageID, err := positiveID(segments[2], "message id")
		if err != nil {
			return LinkRef{}, err
		}
		return LinkRef{ChannelID: channelID, MessageID: messageID}, nil
	}
	if first == "s" && len(segments) == 3 {
		segments = segments[1:]
	}
	if len(segments) != 2 {
		return LinkRef{}, fmt.Errorf("%w: expected t.me/<channel>/<message-id>", ErrInvalidLink)
	}
	username, err := url.PathUnescape(segments[0])
	if err != nil {
		return LinkRef{}, fmt.Errorf("%w: %v", ErrInvalidLink, err)
	}
	if reservedPathSegments[strings.ToLower(username)] {
		return LinkRef{}, fmt.Errorf("%w: %q is not a channel message link", ErrUnsupportedTarget, username)
	}
	if !telegramUsernamePattern.MatchString(username) {
		return LinkRef{}, fmt.Errorf("%w: username %q", ErrInvalidLink, username)
	}
	messageID, err := positiveID(segments[1], "message id")
	if err != nil {
		return LinkRef{}, err
	}
	return LinkRef{Username: username, MessageID: messageID}, nil
}

func positiveID(part, label string) (int64, error) {
	id, err := strconv.ParseInt(part, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("%w: %s %q", ErrInvalidLink, label, part)
	}
	return id, nil
}
