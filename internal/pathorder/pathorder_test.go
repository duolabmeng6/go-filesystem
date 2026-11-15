package pathorder

import "testing"

func TestCompareDepthFirst(t *testing.T) {
	for _, test := range []struct{ earlier, later string }{
		{"a", "a/file"},
		{"a/file", "a.txt"},
		{"a/file", "a-b.txt"},
		{"a/file", "a0.txt"},
		{"a/nested/file", "a/z.txt"},
		{"目录/文件", "目录.txt"},
	} {
		if Compare(test.earlier, test.later) >= 0 || Compare(test.later, test.earlier) <= 0 || Compare(test.earlier, test.earlier) != 0 {
			t.Errorf("unexpected order for %q and %q", test.earlier, test.later)
		}
	}
}
