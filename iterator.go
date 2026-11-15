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

// ErrPaginationCursorLoop indicates that a listing returned a cursor already
// seen by the iterator, so continuing would repeat or cycle through pages.
var ErrPaginationCursorLoop = errors.New("pagination cursor repeated")

type pageIterator struct {
	disk        *Disk
	prefix      string
	opts        ListOptions
	items       []Entry
	seenCursors map[string]struct{}
	terminalErr error
	closed      bool
	done        bool
}

func (it *pageIterator) Next(ctx context.Context) (Entry, error) {
	if it.closed {
		return Entry{}, io.EOF
	}
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	if it.terminalErr != nil {
		return Entry{}, it.terminalErr
	}
	for len(it.items) == 0 {
		if it.done {
			return Entry{}, io.EOF
		}
		if it.seenCursors == nil {
			it.seenCursors = make(map[string]struct{})
			if it.opts.Cursor != "" {
				it.seenCursors[it.opts.Cursor] = struct{}{}
			}
		}
		page, err := it.disk.ListPage(ctx, it.prefix, WithRecursive(it.opts.Recursive), WithPageSize(it.opts.PageSize), WithCursor(it.opts.Cursor))
		if err != nil {
			return Entry{}, err
		}
		if err := ctx.Err(); err != nil {
			return Entry{}, err
		}
		if page.NextCursor != "" {
			if _, seen := it.seenCursors[page.NextCursor]; seen {
				it.terminalErr = newOpError("list", it.disk.name, it.prefix, ErrPaginationCursorLoop)
				return Entry{}, it.terminalErr
			}
			it.seenCursors[page.NextCursor] = struct{}{}
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
	it.seenCursors = nil
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
