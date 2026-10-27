package telegramstore

import (
	"context"
	"fmt"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func TestMigratingPoolRoutesEachFileIndependently(t *testing.T) {
	ctx := WithClientID(context.Background(), 42)
	var charged, homeCalls int
	home := &testDCPool{InvokeFunc: func(_ context.Context, input bin.Encoder, output bin.Decoder) error {
		homeCalls++
		dc := int(input.(*tg.UploadGetFileRequest).Location.(*tg.InputDocumentFileLocation).ID)
		if dc == 1 {
			output.(*tg.UploadFileBox).File = &tg.UploadFile{Bytes: []byte{1}}
			return nil
		}
		return tgerr.New(303, fmt.Sprintf("FILE_MIGRATE_%d", dc))
	}}
	opens := make(map[int]int)
	router := newMigratingPool(home, func(_ context.Context, dc int) (telegram.CloseInvoker, error) {
		opens[dc]++
		return &testDCPool{InvokeFunc: func(gotCtx context.Context, input bin.Encoder, output bin.Decoder) error {
			if gotCtx != ctx {
				t.Error("request context lost")
			}
			if id := input.(*tg.UploadGetFileRequest).Location.(*tg.InputDocumentFileLocation).ID; id != int64(dc) {
				t.Errorf("file %d routed to DC %d", id, dc)
			}
			output.(*tg.UploadFileBox).File = &tg.UploadFile{Bytes: []byte{byte(dc)}}
			return nil
		}}, nil
	}, func(api tg.Invoker) tg.Invoker {
		return telegram.InvokeFunc(func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
			charged++
			return api.Invoke(ctx, input, output)
		})
	})
	defer router.Close()
	for _, dc := range []int{4, 1, 2, 4, 2, 1} {
		var output tg.UploadFileBox
		if err := router.Invoke(ctx, &tg.UploadGetFileRequest{Location: &tg.InputDocumentFileLocation{ID: int64(dc)}}, &output); err != nil {
			t.Fatal(err)
		}
		if got := output.File.(*tg.UploadFile).Bytes[0]; got != byte(dc) {
			t.Fatalf("bytes=%d want %d", got, dc)
		}
	}
	if homeCalls != 4 || opens[2] != 1 || opens[4] != 1 || len(opens) != 2 {
		t.Fatalf("home calls=%d opens=%v", homeCalls, opens)
	}
	if charged != 8 {
		t.Fatalf("middleware calls=%d, want 8 (4 home + 2 redirected + 2 cached)", charged)
	}
}

func TestMigratingPoolRemembersOnlySuccessfulFileRoutes(t *testing.T) {
	var homeCalls, targetCalls int
	home := &testDCPool{InvokeFunc: func(_ context.Context, input bin.Encoder, _ bin.Decoder) error {
		homeCalls++
		if _, ok := input.(*tg.UploadGetFileRequest); !ok {
			return nil
		}
		return tgerr.New(303, "FILE_MIGRATE_4")
	}}
	router := newMigratingPool(home, func(_ context.Context, dc int) (telegram.CloseInvoker, error) {
		return &testDCPool{InvokeFunc: func(context.Context, bin.Encoder, bin.Decoder) error { targetCalls++; return nil }}, nil
	}, unwrappedPool)
	defer router.Close()
	request := &tg.UploadGetFileRequest{Location: &tg.InputDocumentFileLocation{ID: 123, AccessHash: 456}}
	for range 10 {
		if err := router.Invoke(context.Background(), request, &tg.UploadFileBox{}); err != nil {
			t.Fatal(err)
		}
	}
	if homeCalls != 1 || targetCalls != 10 {
		t.Fatalf("home=%d target=%d", homeCalls, targetCalls)
	}
	// A new file and the same ID with a different access hash cannot borrow a route.
	for _, loc := range []*tg.InputDocumentFileLocation{{ID: 124, AccessHash: 456}, {ID: 123, AccessHash: 789}} {
		if err := router.Invoke(context.Background(), &tg.UploadGetFileRequest{Location: loc}, &tg.UploadFileBox{}); err != nil {
			t.Fatal(err)
		}
	}
	if homeCalls != 3 {
		t.Fatalf("distinct file routes: home=%d", homeCalls)
	}
	if err := router.Invoke(context.Background(), &tg.HelpGetConfigRequest{}, &tg.Config{}); err != nil {
		t.Fatal(err)
	}
	if homeCalls != 4 {
		t.Fatal("metadata must stay on home DC")
	}
}
