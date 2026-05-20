package filesystem

type WriteOptions struct {
	Visibility  Visibility
	AtomicWrite bool
	Overwrite   bool
}

type WriteOption func(*WriteOptions)

func DefaultWriteOptions() WriteOptions {
	return WriteOptions{
		AtomicWrite: true,
		Overwrite:   true,
	}
}

func WithVisibility(visibility Visibility) WriteOption {
	return func(o *WriteOptions) {
		o.Visibility = visibility
	}
}

func WithAtomicWrite(enabled bool) WriteOption {
	return func(o *WriteOptions) {
		o.AtomicWrite = enabled
	}
}

func WithOverwrite(enabled bool) WriteOption {
	return func(o *WriteOptions) {
		o.Overwrite = enabled
	}
}

func applyWriteOptions(opts []WriteOption) (WriteOptions, error) {
	o := DefaultWriteOptions()
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	if !o.Visibility.Valid() {
		return o, ErrInvalidVisibility
	}
	return o, nil
}

type DeleteOptions struct {
	IgnoreMissing bool
}

type DeleteOption func(*DeleteOptions)

func WithIgnoreMissing() DeleteOption {
	return func(o *DeleteOptions) {
		o.IgnoreMissing = true
	}
}

func applyDeleteOptions(opts []DeleteOption) DeleteOptions {
	var o DeleteOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	return o
}

type DirectoryOptions struct {
	Visibility Visibility
}

type DirectoryOption func(*DirectoryOptions)

func applyDirectoryOptions(opts []DirectoryOption) (DirectoryOptions, error) {
	var o DirectoryOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	if !o.Visibility.Valid() {
		return o, ErrInvalidVisibility
	}
	return o, nil
}

type ListOption func(*ListOptions)

func WithRecursive(recursive bool) ListOption {
	return func(o *ListOptions) {
		o.Recursive = recursive
	}
}

func WithPageSize(size int) ListOption {
	return func(o *ListOptions) {
		o.PageSize = size
	}
}

func WithCursor(cursor string) ListOption {
	return func(o *ListOptions) {
		o.Cursor = cursor
	}
}

func applyListOptions(opts []ListOption) ListOptions {
	var o ListOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	return o
}

type URLOptions struct {
	Parameters map[string]string
}

type URLOption func(*URLOptions)

func WithURLParameter(key, value string) URLOption {
	return func(o *URLOptions) {
		if o.Parameters == nil {
			o.Parameters = make(map[string]string)
		}
		o.Parameters[key] = value
	}
}

func applyURLOptions(opts []URLOption) URLOptions {
	var o URLOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	return o
}
