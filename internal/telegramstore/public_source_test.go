package telegramstore

import (
	"context"
	"fmt"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"testing"
)

type publicSourceInvoker struct {
	channel                     int64
	hash                        int64
	usernameCalls, messageCalls int
}

func (i *publicSourceInvoker) Invoke(_ context.Context, input bin.Encoder, output bin.Decoder) error {
	switch r := input.(type) {
	case *tg.ContactsResolveUsernameRequest:
		i.usernameCalls++
		if r.Username != "synthetic_channel" {
			return fmt.Errorf("unexpected username")
		}
		*output.(*tg.ContactsResolvedPeer) = tg.ContactsResolvedPeer{Chats: []tg.ChatClass{&tg.Channel{ID: i.channel, AccessHash: i.hash}}}
		return nil
	case *tg.ChannelsGetMessagesRequest:
		i.messageCalls++
		c := r.Channel.(*tg.InputChannel)
		if c.ChannelID != i.channel || c.AccessHash != i.hash {
			return fmt.Errorf("lost resolved access hash")
		}
		output.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{&tg.Message{ID: 7, Media: &tg.MessageMediaDocument{Document: &tg.Document{ID: 99, AccessHash: 555, Size: 1234}}}}}
		return nil
	}
	return fmt.Errorf("unexpected RPC %T", input)
}
func TestPublicSourceRetainsResolvedHashAndChecksChannel(t *testing.T) {
	for _, match := range []bool{true, false} {
		i := &publicSourceInvoker{channel: 42, hash: 987}
		id := int64(42)
		if !match {
			id = 43
		}
		loc, size, err := sourceDocumentLocation(WithClientID(context.Background(), 100), tg.NewClient(i), id, 7, "synthetic_channel", nil)
		if match {
			if err != nil || loc == nil || size != 1234 || i.messageCalls != 1 {
				t.Fatalf("location=%v size=%d err=%v calls=%d", loc, size, err, i.messageCalls)
			}
		} else if err != ErrInvalidChannel || i.messageCalls != 0 {
			t.Fatal("mismatched channel accepted")
		}
		if i.usernameCalls != 1 {
			t.Fatal("not resolved per owner request")
		}
	}
}
