package filesystem

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrDiskNotFound            = errors.New("disk not found")
	ErrDriverNotFound          = errors.New("driver not found")
	ErrDriverAlreadyRegistered = errors.New("driver already registered")
	ErrDiskAlreadyRegistered   = errors.New("disk already registered")
	ErrInvalidPath             = errors.New("invalid path")
	ErrNotFound                = errors.New("file not found")
	ErrAlreadyExists           = errors.New("file already exists")
	ErrUnsupported             = errors.New("operation unsupported")
	ErrReadOnly                = errors.New("disk is read-only")
	ErrPartialFailure          = errors.New("partial failure")
	ErrInvalidVisibility       = errors.New("invalid visibility")
	ErrInvalidExpiration       = errors.New("invalid expiration")
)

type OpError struct {
	Op      string
	Disk    string
	Path    string
	Err     error
	Partial bool
}

func (e *OpError) Error() string {
	if e == nil {
		return "<nil>"
	}
	var b strings.Builder
	if e.Op != "" {
		b.WriteString(e.Op)
	} else {
		b.WriteString("filesystem operation")
	}
	if e.Disk != "" {
		b.WriteString(" on disk ")
		b.WriteString(e.Disk)
	}
	if e.Path != "" {
		b.WriteString(" for ")
		b.WriteString(e.Path)
	}
	if e.Partial {
		b.WriteString(" partially failed")
	}
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

func (e *OpError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *OpError) Is(target error) bool {
	if e == nil {
		return false
	}
	return e.Partial && target == ErrPartialFailure
}

type PathError struct {
	Path string
	Err  error
}

func (e PathError) Error() string {
	if e.Path == "" {
		return e.Err.Error()
	}
	return fmt.Sprintf("%s: %v", e.Path, e.Err)
}

func (e PathError) Unwrap() error {
	return e.Err
}

type MultiError struct {
	Op     string
	Errors []PathError
}

func (e *MultiError) Error() string {
	if e == nil || len(e.Errors) == 0 {
		return "<nil>"
	}
	op := e.Op
	if op == "" {
		op = "multiple filesystem operations"
	}
	return fmt.Sprintf("%s failed for %d path(s): %v", op, len(e.Errors), e.Errors[0])
}

func (e *MultiError) Unwrap() []error {
	if e == nil || len(e.Errors) == 0 {
		return nil
	}
	errs := make([]error, 0, len(e.Errors))
	for _, pathErr := range e.Errors {
		if pathErr.Err != nil {
			errs = append(errs, pathErr.Err)
		}
	}
	return errs
}

func (e *MultiError) Is(target error) bool {
	if e == nil {
		return false
	}
	for _, pathErr := range e.Errors {
		if errors.Is(pathErr.Err, target) {
			return true
		}
	}
	return false
}

func newOpError(op, disk, path string, err error) error {
	if err == nil {
		return nil
	}
	return &OpError{Op: op, Disk: disk, Path: path, Err: err}
}
