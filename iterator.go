package filesystem

import (
	"context"
	"errors"
	"io"
)

type EntryIterator interface {
	Next(ctx context.Context) (Entry, error)
	Close() error
}

type pageIterator struct {
	disk   *Disk
	prefix string
	opts   ListOptions
	items  []Entry
	closed bool
	done   bool
}

func (it *pageIterator) Next(ctx context.Context) (Entry, error) {
	if it.closed {
		return Entry{}, io.EOF
	}
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	for len(it.items) == 0 {
		if it.done {
			return Entry{}, io.EOF
		}
		page, err := it.disk.ListPage(ctx, it.prefix, WithRecursive(it.opts.Recursive), WithPageSize(it.opts.PageSize), WithCursor(it.opts.Cursor))
		if err != nil {
			return Entry{}, err
		}
		it.items = page.Items
		it.opts.Cursor = page.NextCursor
		if page.NextCursor == "" {
			it.done = true
		}
	}
	entry := it.items[0]
	it.items = it.items[1:]
	return entry, nil
}

func (it *pageIterator) Close() error {
	it.closed = true
	it.items = nil
	return nil
}

func collectIterator(ctx context.Context, it EntryIterator, typ EntryType) ([]string, error) {
	defer it.Close()
	var paths []string
	for {
		entry, err := it.Next(ctx)
		if errors.Is(err, io.EOF) {
			return paths, nil
		}
		if err != nil {
			return nil, err
		}
		if entry.Type == typ {
			paths = append(paths, entry.Path)
		}
	}
}
