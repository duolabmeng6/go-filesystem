package filesystem

import (
	"errors"
	"testing"
)

func TestNormalizePath(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		err  error
	}{
		{name: "empty root", in: "", want: ""},
		{name: "simple", in: "avatars/user.png", want: "avatars/user.png"},
		{name: "absolute", in: "/etc/passwd", err: ErrInvalidPath},
		{name: "dot", in: "a/./b", err: ErrInvalidPath},
		{name: "dot dot", in: "a/../b", err: ErrInvalidPath},
		{name: "double slash", in: "a//b", err: ErrInvalidPath},
		{name: "nul", in: "a\x00b", err: ErrInvalidPath},
		{name: "backslash", in: `a\b`, err: ErrInvalidPath},
		{name: "drive backslash", in: `C:\Windows`, err: ErrInvalidPath},
		{name: "drive slash", in: "C:/Windows", err: ErrInvalidPath},
		{name: "unc", in: `\\server\share`, err: ErrInvalidPath},
		{name: "colon segment", in: "a:b", err: ErrInvalidPath},
		{name: "reserved", in: "CON", err: ErrInvalidPath},
		{name: "reserved extension", in: "aux.txt", err: ErrInvalidPath},
		{name: "unicode", in: "你好/文件.txt", want: "你好/文件.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizePath(tt.in)
			if tt.err != nil {
				if !errors.Is(err, tt.err) {
					t.Fatalf("expected %v, got %v", tt.err, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizePath: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func FuzzNormalizePath(f *testing.F) {
	for _, seed := range []string{"a.txt", "../x", "/x", `C:\Windows`, "你好.txt", "a%2Fb", "CON"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, p string) {
		got, err := NormalizePath(p)
		if err != nil {
			return
		}
		if got == "." || got == ".." {
			t.Fatalf("bad normalized path %q", got)
		}
		if len(got) > 0 && got[0] == '/' {
			t.Fatalf("absolute normalized path %q", got)
		}
	})
}
