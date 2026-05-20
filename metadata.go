package filesystem

import "time"

type FileInfo struct {
	Path         string
	Size         int64
	LastModified time.Time
	IsDir        bool
}

type EntryType string

const (
	EntryFile      EntryType = "file"
	EntryDirectory EntryType = "directory"
)

type Entry struct {
	Path         string
	Type         EntryType
	Size         int64
	LastModified time.Time
}

type ListOptions struct {
	Recursive bool
	PageSize  int
	Cursor    string
}

type Page struct {
	Items      []Entry
	NextCursor string
}
