package s3

import (
	"context"
	"errors"
	"testing"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type metadataListClient struct {
	*mockClient
	pages []*awss3.ListPartsOutput
	calls int
}

func (m *metadataListClient) ListParts(_ context.Context, _ *awss3.ListPartsInput, _ ...func(*awss3.Options)) (*awss3.ListPartsOutput, error) {
	if m.calls >= len(m.pages) {
		return nil, errors.New("unexpected extra page")
	}
	page := m.pages[m.calls]
	m.calls++
	return page, nil
}

func TestListMultipartUploadPartsRejectsInvalidMetadataAndPagination(t *testing.T) {
	number := int32(1)
	size := int64(0)
	etag := "etag"
	valid := types.Part{PartNumber: &number, Size: &size, ETag: &etag}
	negative := int64(-1)
	badNumber := int32(0)
	tests := []struct {
		name  string
		pages []*awss3.ListPartsOutput
	}{
		{"nil response", []*awss3.ListPartsOutput{nil}},
		{"missing pagination status", []*awss3.ListPartsOutput{{}}},
		{"missing number", []*awss3.ListPartsOutput{{IsTruncated: boolPtr(false), Parts: []types.Part{{Size: &size, ETag: &etag}}}}},
		{"invalid number", []*awss3.ListPartsOutput{{IsTruncated: boolPtr(false), Parts: []types.Part{{PartNumber: &badNumber, Size: &size, ETag: &etag}}}}},
		{"missing size", []*awss3.ListPartsOutput{{IsTruncated: boolPtr(false), Parts: []types.Part{{PartNumber: &number, ETag: &etag}}}}},
		{"negative size", []*awss3.ListPartsOutput{{IsTruncated: boolPtr(false), Parts: []types.Part{{PartNumber: &number, Size: &negative, ETag: &etag}}}}},
		{"missing etag", []*awss3.ListPartsOutput{{IsTruncated: boolPtr(false), Parts: []types.Part{{PartNumber: &number, Size: &size}}}}},
		{"empty etag", []*awss3.ListPartsOutput{{IsTruncated: boolPtr(false), Parts: []types.Part{{PartNumber: &number, Size: &size, ETag: stringPtr(" ")}}}}},
		{"missing marker", []*awss3.ListPartsOutput{{IsTruncated: boolPtr(true), Parts: []types.Part{valid}}}},
		{"invalid marker", []*awss3.ListPartsOutput{{IsTruncated: boolPtr(true), NextPartNumberMarker: stringPtr("invalid"), Parts: []types.Part{valid}}}},
		{"marker skips results", []*awss3.ListPartsOutput{{IsTruncated: boolPtr(true), NextPartNumberMarker: stringPtr("2"), Parts: []types.Part{valid}}}},
		{"duplicate part", []*awss3.ListPartsOutput{{IsTruncated: boolPtr(false), Parts: []types.Part{valid, valid}}}},
		{"marker not advanced", []*awss3.ListPartsOutput{{IsTruncated: boolPtr(true), NextPartNumberMarker: stringPtr("1"), Parts: []types.Part{valid}}, {IsTruncated: boolPtr(true), NextPartNumberMarker: stringPtr("1")}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &metadataListClient{mockClient: newMockClient(), pages: tt.pages}
			adapter := &Adapter{client: client, bucket: "bucket"}
			parts, err := adapter.ListMultipartUploadParts(context.Background(), "file", "upload")
			if err == nil || parts != nil {
				t.Fatalf("expected error without partial results, got parts=%v err=%v", parts, err)
			}
		})
	}
}

func TestListMultipartUploadPartsAcceptsEmptyPartAndPagination(t *testing.T) {
	one, two := int32(1), int32(2)
	zero, five := int64(0), int64(5)
	client := &metadataListClient{mockClient: newMockClient(), pages: []*awss3.ListPartsOutput{
		{IsTruncated: boolPtr(true), NextPartNumberMarker: stringPtr("1"), Parts: []types.Part{{PartNumber: &one, Size: &zero, ETag: stringPtr("empty")}}},
		{IsTruncated: boolPtr(false), Parts: []types.Part{{PartNumber: &two, Size: &five, ETag: stringPtr("five")}}},
	}}
	adapter := &Adapter{client: client, bucket: "bucket"}
	parts, err := adapter.ListMultipartUploadParts(context.Background(), "file", "upload")
	if err != nil || len(parts) != 2 || parts[0].Size != 0 || parts[1].Size != 5 || client.calls != 2 {
		t.Fatalf("unexpected parts=%v calls=%d err=%v", parts, client.calls, err)
	}
}
