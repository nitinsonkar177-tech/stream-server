package telegramstore

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

var errMigratingPoolClosed = errors.New("Telegram connection pool closed")

type dcPoolEntry struct {
	ready chan struct{}
	pool  telegram.CloseInvoker
	api   tg.Invoker
	err   error
}

// migratingPool follows file redirects without migrating the primary session.
// File locations remember their own DC after a successful redirect. Metadata
// requests and unknown files still start on the home DC.
type fileRouteKey struct {
	id         int64
	accessHash int64
}

func fileRoute(input bin.Encoder) (fileRouteKey, bool) {
	request, ok := input.(*tg.UploadGetFileRequest)
	if !ok || request == nil {
		return fileRouteKey{}, false
	}
	location, ok := request.Location.(*tg.InputDocumentFileLocation)
	if !ok || location == nil || location.ID == 0 {
		return fileRouteKey{}, false
	}
	return fileRouteKey{id: location.ID, accessHash: location.AccessHash}, true
}

type migratingPool struct {
	home    telegram.CloseInvoker
	api     tg.Invoker
	open    func(context.Context, int) (telegram.CloseInvoker, error)
	wrap    func(tg.Invoker) tg.Invoker
	mu      sync.Mutex
	pools   map[int]*dcPoolEntry
	fileDCs map[fileRouteKey]int
	closed  bool
}

func newMigratingPool(home telegram.CloseInvoker, open func(context.Context, int) (telegram.CloseInvoker, error), wrap func(tg.Invoker) tg.Invoker) *migratingPool {
	return &migratingPool{home: home, api: wrap(home), open: open, wrap: wrap, pools: make(map[int]*dcPoolEntry), fileDCs: make(map[fileRouteKey]int)}
}

func (p *migratingPool) Invoke(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
	p.mu.Lock()
	closed := p.closed
	key, cacheable := fileRoute(input)
	dc := p.fileDCs[key]
	p.mu.Unlock()
	if closed {
		return errMigratingPoolClosed
	}
	api := p.api
	if cacheable && dc > 0 {
		var err error
		api, err = p.target(ctx, dc)
		if err != nil {
			return err
		}
	}
	err := api.Invoke(ctx, input, output)
	rpcErr, ok := tgerr.As(err)
	if !ok || rpcErr.Type != "FILE_MIGRATE" || rpcErr.Argument <= 0 {
		return err
	}
	api, openErr := p.target(ctx, rpcErr.Argument)
	if openErr != nil {
		return fmt.Errorf("open Telegram file DC %d: %w", rpcErr.Argument, openErr)
	}
	// Retry once, not recursively: malformed redirects must not loop forever.
	err = api.Invoke(ctx, input, output)
	if err == nil && cacheable {
		p.mu.Lock()
		// Bound memory even when a pooled client serves many different files.
		if len(p.fileDCs) >= 4096 {
			p.fileDCs = make(map[fileRouteKey]int)
		}
		p.fileDCs[key] = rpcErr.Argument
		p.mu.Unlock()
	}
	return err
}

func (p *migratingPool) target(ctx context.Context, dc int) (tg.Invoker, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, errMigratingPoolClosed
	}
	if entry, ok := p.pools[dc]; ok {
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-entry.ready:
			return entry.api, entry.err
		}
	}
	entry := &dcPoolEntry{ready: make(chan struct{})}
	p.pools[dc] = entry
	p.mu.Unlock()

	pool, err := p.open(ctx, dc)
	p.mu.Lock()
	if p.closed && err == nil {
		err = errors.Join(errMigratingPoolClosed, pool.Close())
	}
	if err != nil {
		// A cancelled request or failed authorization must not poison later reads.
		delete(p.pools, dc)
	} else {
		entry.pool, entry.api = pool, p.wrap(pool)
	}
	entry.err = err
	close(entry.ready)
	p.mu.Unlock()
	return entry.api, entry.err
}

func (p *migratingPool) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	var pools []telegram.CloseInvoker
	for _, entry := range p.pools {
		if entry.pool != nil {
			pools = append(pools, entry.pool)
		}
	}
	p.mu.Unlock()
	err := p.home.Close()
	for _, pool := range pools {
		err = errors.Join(err, pool.Close())
	}
	return err
}
