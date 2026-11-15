package oss

import (
	"context"
	"errors"
	"testing"

	aliyunoss "github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
)

type metadataListClient struct {
	*mockClient
	pages []*aliyunoss.ListPartsResult
	calls int
}

func (m *metadataListClient) ListParts(_ context.Context, _ *aliyunoss.ListPartsRequest, _ ...func(*aliyunoss.Options)) (*aliyunoss.ListPartsResult, error) {
	if m.calls >= len(m.pages) {
		return nil, errors.New("unexpected extra page")
	}
	page := m.pages[m.calls]
	m.calls++
	return page, nil
}

func TestListMultipartUploadPartsRejectsInvalidMetadataAndPagination(t *testing.T) {
	valid := aliyunoss.Part{PartNumber: 1, Size: 0, ETag: aliyunoss.Ptr("etag")}
	tests := []struct {
		name  string
		pages []*aliyunoss.ListPartsResult
	}{
		{"nil response", []*aliyunoss.ListPartsResult{nil}},
		{"missing number", []*aliyunoss.ListPartsResult{{Parts: []aliyunoss.Part{{Size: 1, ETag: aliyunoss.Ptr("etag")}}}}},
		{"negative size", []*aliyunoss.ListPartsResult{{Parts: []aliyunoss.Part{{PartNumber: 1, Size: -1, ETag: aliyunoss.Ptr("etag")}}}}},
		{"missing etag", []*aliyunoss.ListPartsResult{{Parts: []aliyunoss.Part{{PartNumber: 1, Size: 0}}}}},
		{"empty etag", []*aliyunoss.ListPartsResult{{Parts: []aliyunoss.Part{{PartNumber: 1, ETag: aliyunoss.Ptr(" ")}}}}},
		{"missing marker", []*aliyunoss.ListPartsResult{{IsTruncated: true, Parts: []aliyunoss.Part{valid}}}},
		{"marker skips results", []*aliyunoss.ListPartsResult{{IsTruncated: true, NextPartNumberMarker: 2, Parts: []aliyunoss.Part{valid}}}},
		{"duplicate part", []*aliyunoss.ListPartsResult{{Parts: []aliyunoss.Part{valid, valid}}}},
		{"marker not advanced", []*aliyunoss.ListPartsResult{{IsTruncated: true, NextPartNumberMarker: 1, Parts: []aliyunoss.Part{valid}}, {IsTruncated: true, NextPartNumberMarker: 1}}},
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
	client := &metadataListClient{mockClient: newMockClient(), pages: []*aliyunoss.ListPartsResult{
		{IsTruncated: true, NextPartNumberMarker: 1, Parts: []aliyunoss.Part{{PartNumber: 1, Size: 0, ETag: aliyunoss.Ptr("empty")}}},
		{Parts: []aliyunoss.Part{{PartNumber: 2, Size: 5, ETag: aliyunoss.Ptr("five")}}},
	}}
	adapter := &Adapter{client: client, bucket: "bucket"}
	parts, err := adapter.ListMultipartUploadParts(context.Background(), "file", "upload")
	if err != nil || len(parts) != 2 || parts[0].Size != 0 || parts[1].Size != 5 || client.calls != 2 {
		t.Fatalf("unexpected parts=%v calls=%d err=%v", parts, client.calls, err)
	}
}
