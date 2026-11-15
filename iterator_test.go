package filesystem

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
)

type cursorTestAdapter struct {
	*memoryAdapter
	pages  map[string]Page
	calls  []string
	onList func(context.Context, ListOptions) (Page, error)
}

func (a *cursorTestAdapter) ListPage(ctx context.Context, _ string, opts ListOptions) (Page, error) {
	a.calls = append(a.calls, opts.Cursor)
	if a.onList != nil {
		return a.onList(ctx, opts)
	}
	return a.pages[opts.Cursor], nil
}

func TestPageIteratorRejectsRepeatedCursorBeforeReturningPage(t *testing.T) {
	adapter := &cursorTestAdapter{memoryAdapter: newMemoryAdapter(), pages: map[string]Page{
		"":     {Items: []Entry{{Path: "first", Type: EntryFile}}, NextCursor: "next"},
		"next": {Items: []Entry{{Path: "duplicate", Type: EntryFile}}, NextCursor: "next"},
	}}
	it, err := NewDisk(adapter).List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	entry, err := it.Next(context.Background())
	if err != nil || entry.Path != "first" {
		t.Fatalf("first entry = %#v, %v", entry, err)
	}
	if _, err := it.Next(context.Background()); !errors.Is(err, ErrPaginationCursorLoop) {
		t.Fatalf("repeated cursor error = %v", err)
	}
	if _, err := it.Next(context.Background()); !errors.Is(err, ErrPaginationCursorLoop) {
		t.Fatalf("terminal error = %v", err)
	}
	if !reflect.DeepEqual(adapter.calls, []string{"", "next"}) {
		t.Fatalf("page calls = %q", adapter.calls)
	}
}

func TestPageIteratorDetectsCursorCycleOnEmptyPages(t *testing.T) {
	adapter := &cursorTestAdapter{memoryAdapter: newMemoryAdapter(), pages: map[string]Page{
		"":  {NextCursor: "a"},
		"a": {NextCursor: "b"},
		"b": {NextCursor: "a"},
	}}
	it, err := NewDisk(adapter).List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	if _, err := it.Next(context.Background()); !errors.Is(err, ErrPaginationCursorLoop) {
		t.Fatalf("cycle error = %v", err)
	}
	if !reflect.DeepEqual(adapter.calls, []string{"", "a", "b"}) {
		t.Fatalf("page calls = %q", adapter.calls)
	}
}

func TestPageIteratorDetectsCycleToInitialCursor(t *testing.T) {
	adapter := &cursorTestAdapter{memoryAdapter: newMemoryAdapter(), pages: map[string]Page{
		"start": {NextCursor: "next"},
		"next":  {NextCursor: "start"},
	}}
	it, err := NewDisk(adapter).List(context.Background(), "", WithCursor("start"))
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	if _, err := it.Next(context.Background()); !errors.Is(err, ErrPaginationCursorLoop) {
		t.Fatalf("cycle error = %v", err)
	}
}

func TestPageIteratorAcceptsEmptyAdvancingPages(t *testing.T) {
	adapter := &cursorTestAdapter{memoryAdapter: newMemoryAdapter(), pages: map[string]Page{
		"":  {NextCursor: "a"},
		"a": {Items: []Entry{{Path: "one", Type: EntryFile}}, NextCursor: "b"},
		"b": {NextCursor: "c"},
		"c": {Items: []Entry{{Path: "two", Type: EntryFile}}},
	}}
	paths, err := NewDisk(adapter).AllFiles(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, []string{"one", "two"}) {
		t.Fatalf("paths = %q", paths)
	}
	if !reflect.DeepEqual(adapter.calls, []string{"", "a", "b", "c"}) {
		t.Fatalf("page calls = %q", adapter.calls)
	}
}

func TestPageIteratorCancellationAfterPage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	adapter := &cursorTestAdapter{memoryAdapter: newMemoryAdapter()}
	adapter.onList = func(context.Context, ListOptions) (Page, error) {
		cancel()
		return Page{Items: []Entry{{Path: "must-not-return"}}, NextCursor: "next"}, nil
	}
	it, err := NewDisk(adapter).List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	if _, err := it.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestPageIteratorPageErrorCanRetry(t *testing.T) {
	transientErr := errors.New("temporary error")
	adapter := &cursorTestAdapter{memoryAdapter: newMemoryAdapter()}
	adapter.onList = func(context.Context, ListOptions) (Page, error) {
		if len(adapter.calls) == 1 {
			return Page{}, transientErr
		}
		return Page{Items: []Entry{{Path: "ok"}}}, nil
	}
	it, err := NewDisk(adapter).List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	if _, err := it.Next(context.Background()); !errors.Is(err, transientErr) {
		t.Fatalf("first error = %v", err)
	}
	if entry, err := it.Next(context.Background()); err != nil || entry.Path != "ok" {
		t.Fatalf("retry entry = %#v, %v", entry, err)
	}
	if _, err := it.Next(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("final error = %v", err)
	}
}
