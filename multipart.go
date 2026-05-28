package filesystem

type MultipartUpload struct {
	UploadID    string
	Path        string
	MinPartSize int64
	MaxPartSize int64
	MaxParts    int
}

type MultipartUploadPart struct {
	PartNumber int
	ETag       string
	Size       int64
}
