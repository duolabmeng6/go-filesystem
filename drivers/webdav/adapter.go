package webdav

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/duolabmeng6/go-filesystem"
)

type Adapter struct {
	base               *url.URL
	username, password string
	client             *http.Client
}

func ParseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid WebDAV address")
	}
	if strings.Contains(u.Path, "\\") || strings.Contains(u.Path, "//") || strings.ContainsRune(u.Path, 0) || path.Clean(u.Path) != strings.TrimRight(u.Path, "/") && u.Path != "" && u.Path != "/" {
		return nil, fmt.Errorf("invalid WebDAV address path")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/"
	u.RawPath = ""
	return u, nil
}
func NewFactory() filesystem.DriverFactory {
	return func(_ context.Context, cfg filesystem.DiskConfig) (filesystem.Adapter, error) {
		get := func(k string) string { v, _ := cfg.Options[k].(string); return v }
		u, err := ParseURL(get("url"))
		if err != nil {
			return nil, err
		}
		return &Adapter{base: u, username: get("username"), password: get("password"), client: &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
	}
}
func (a *Adapter) Capabilities() filesystem.CapabilitySet {
	return filesystem.NewCapabilitySet(filesystem.CapabilityDirectory, filesystem.CapabilityCopy, filesystem.CapabilityMove)
}
func (a *Adapter) DirectorySemantics() filesystem.DirectorySemantics { return filesystem.DirectoryReal }
func (a *Adapter) target(p string) (string, error) {
	n, e := filesystem.NormalizePath(p)
	if e != nil {
		return "", e
	}
	u := *a.base
	u.Path = a.base.Path + n
	u.RawPath = ""
	return u.String(), nil
}
func (a *Adapter) request(ctx context.Context, method, p string, body io.Reader, headers map[string]string) (*http.Response, error) {
	target, e := a.target(p)
	if e != nil {
		return nil, e
	}
	req, e := http.NewRequestWithContext(ctx, method, target, body)
	if e != nil {
		return nil, e
	}
	if f, ok := body.(*os.File); ok {
		if stat, err := f.Stat(); err == nil {
			req.ContentLength = stat.Size()
		}
	}
	req.SetBasicAuth(a.username, a.password)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, e := a.client.Do(req)
	if e != nil {
		return nil, fmt.Errorf("webdav connection: %w", e)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		res.Body.Close()
		switch res.StatusCode {
		case 404:
			return nil, filesystem.ErrNotFound
		case 412:
			return nil, filesystem.ErrAlreadyExists
		case 401, 403:
			return nil, fmt.Errorf("webdav authentication: HTTP %d", res.StatusCode)
		default:
			return nil, fmt.Errorf("webdav request: HTTP %d", res.StatusCode)
		}
	}
	if res.StatusCode == http.StatusMultiStatus && method != "PROPFIND" {
		var result struct {
			Responses []struct {
				Status string `xml:"status"`
			} `xml:"response"`
		}
		err := xml.NewDecoder(io.LimitReader(res.Body, 32<<20)).Decode(&result)
		res.Body.Close()
		if err != nil || len(result.Responses) == 0 {
			return nil, fmt.Errorf("webdav invalid multistatus response")
		}
		for _, item := range result.Responses {
			fields := strings.Fields(item.Status)
			if len(fields) < 2 || len(fields[1]) != 3 || fields[1][0] != '2' {
				return nil, fmt.Errorf("webdav operation partially failed")
			}
		}
		res.Body = io.NopCloser(strings.NewReader(""))
	}
	return res, nil
}

type multistatus struct {
	Responses []struct {
		Href      string `xml:"href"`
		Propstats []struct {
			Status string `xml:"status"`
			Prop   struct {
				Size     int64  `xml:"getcontentlength"`
				Modified string `xml:"getlastmodified"`
				Resource struct {
					Collection *struct{} `xml:"collection"`
				} `xml:"resourcetype"`
			} `xml:"prop"`
		} `xml:"propstat"`
	} `xml:"response"`
}

func (a *Adapter) propfind(ctx context.Context, p, depth string) ([]filesystem.FileInfo, error) {
	res, e := a.request(ctx, "PROPFIND", p, strings.NewReader(`<?xml version="1.0"?><propfind xmlns="DAV:"><prop><resourcetype/><getcontentlength/><getlastmodified/></prop></propfind>`), map[string]string{"Depth": depth, "Content-Type": "application/xml"})
	if e != nil {
		return nil, e
	}
	defer res.Body.Close()
	var data multistatus
	if e = xml.NewDecoder(io.LimitReader(res.Body, 32<<20)).Decode(&data); e != nil {
		return nil, fmt.Errorf("webdav response: %w", e)
	}
	items := make([]filesystem.FileInfo, 0, len(data.Responses))
	for _, v := range data.Responses {
		u, e := url.Parse(v.Href)
		if e != nil {
			return nil, e
		}
		if u.IsAbs() && !strings.EqualFold(u.Host, a.base.Host) {
			return nil, fmt.Errorf("webdav response outside root")
		}
		decoded := u.Path
		if decoded == strings.TrimSuffix(a.base.Path, "/") {
			decoded = a.base.Path
		}
		if !strings.HasPrefix(decoded, a.base.Path) {
			return nil, fmt.Errorf("webdav response outside root")
		}
		name := strings.TrimSuffix(strings.TrimPrefix(decoded, a.base.Path), "/")
		norm, e := filesystem.NormalizePath(name)
		if e != nil || norm != name {
			return nil, filesystem.ErrInvalidPath
		}
		found := false
		for _, ps := range v.Propstats {
			if !strings.Contains(ps.Status, " 200 ") {
				continue
			}
			modified, _ := http.ParseTime(ps.Prop.Modified)
			items = append(items, filesystem.FileInfo{Path: name, Size: ps.Prop.Size, LastModified: modified, IsDir: ps.Prop.Resource.Collection != nil})
			found = true
			break
		}
		if !found {
			return nil, fmt.Errorf("webdav response missing properties")
		}
	}
	return items, nil
}
func (a *Adapter) Stat(ctx context.Context, p string) (filesystem.FileInfo, error) {
	n, e := filesystem.NormalizePath(p)
	if e != nil {
		return filesystem.FileInfo{}, e
	}
	items, e := a.propfind(ctx, n, "0")
	if e != nil {
		return filesystem.FileInfo{}, e
	}
	for _, v := range items {
		if v.Path == n {
			return v, nil
		}
	}
	return filesystem.FileInfo{}, filesystem.ErrNotFound
}
func (a *Adapter) Exists(ctx context.Context, p string) (bool, error) {
	_, e := a.Stat(ctx, p)
	if errors.Is(e, filesystem.ErrNotFound) {
		return false, nil
	}
	return e == nil, e
}
func (a *Adapter) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	res, e := a.request(ctx, "GET", p, nil, nil)
	if e != nil {
		return nil, e
	}
	return res.Body, nil
}
func (a *Adapter) Write(ctx context.Context, p string, r io.Reader, o filesystem.WriteOptions) error {
	n, e := filesystem.NormalizePath(p)
	if e != nil || n == "" || r == nil {
		return filesystem.ErrInvalidPath
	}
	if !o.Visibility.Valid() {
		return filesystem.ErrInvalidVisibility
	}
	if e = a.ensureParent(ctx, n); e != nil {
		return e
	}
	if !o.Overwrite {
		exists, err := a.Exists(ctx, n)
		if err != nil {
			return err
		}
		if exists {
			return filesystem.ErrAlreadyExists
		}
	}
	// A known Content-Length is required by some WebDAV servers, including cloud providers.
	f, e := os.CreateTemp("", "afile-webdav-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, e = io.Copy(f, &contextReader{ctx: ctx, reader: r}); e != nil {
		return e
	}
	if _, e = f.Seek(0, io.SeekStart); e != nil {
		return e
	}
	h := map[string]string{}
	if !o.Overwrite {
		h["If-None-Match"] = "*"
	}
	res, e := a.request(ctx, "PUT", n, f, h)
	if e != nil {
		return e
	}
	return res.Body.Close()
}
func (a *Adapter) ensureParent(ctx context.Context, p string) error {
	dir := path.Dir(p)
	if dir == "." {
		return nil
	}
	return a.MakeDirectory(ctx, dir, filesystem.DirectoryOptions{})
}
func (a *Adapter) MakeDirectory(ctx context.Context, p string, o filesystem.DirectoryOptions) error {
	if !o.Visibility.Valid() {
		return filesystem.ErrInvalidVisibility
	}
	n, e := filesystem.NormalizePath(p)
	if e != nil {
		return e
	}
	if n == "" {
		return nil
	}
	cur := ""
	for _, part := range strings.Split(n, "/") {
		cur = path.Join(cur, part)
		info, e := a.Stat(ctx, cur)
		if e == nil {
			if !info.IsDir {
				return filesystem.ErrInvalidPath
			}
			continue
		}
		if !errors.Is(e, filesystem.ErrNotFound) {
			return e
		}
		res, e := a.request(ctx, "MKCOL", cur, nil, nil)
		if e != nil {
			return e
		}
		res.Body.Close()
	}
	return nil
}
func (a *Adapter) remove(ctx context.Context, p string, dir bool) error {
	n, e := filesystem.NormalizePath(p)
	if e != nil || n == "" {
		return filesystem.ErrInvalidPath
	}
	info, e := a.Stat(ctx, n)
	if e != nil {
		return e
	}
	if info.IsDir != dir {
		return filesystem.ErrInvalidPath
	}
	res, e := a.request(ctx, "DELETE", n, nil, nil)
	if e != nil {
		return e
	}
	return res.Body.Close()
}
func (a *Adapter) Delete(ctx context.Context, p string) error          { return a.remove(ctx, p, false) }
func (a *Adapter) DeleteDirectory(ctx context.Context, p string) error { return a.remove(ctx, p, true) }
func (a *Adapter) transfer(ctx context.Context, method, src, dst string) error {
	s, e := filesystem.NormalizePath(src)
	if e != nil || s == "" {
		return filesystem.ErrInvalidPath
	}
	d, e := filesystem.NormalizePath(dst)
	if e != nil || d == "" {
		return filesystem.ErrInvalidPath
	}
	if e = a.ensureParent(ctx, d); e != nil {
		return e
	}
	target, e := a.target(d)
	if e != nil {
		return e
	}
	res, e := a.request(ctx, method, s, nil, map[string]string{"Destination": target, "Overwrite": "T"})
	if e != nil {
		return e
	}
	return res.Body.Close()
}
func (a *Adapter) Copy(ctx context.Context, s, d string) error { return a.transfer(ctx, "COPY", s, d) }
func (a *Adapter) Move(ctx context.Context, s, d string) error { return a.transfer(ctx, "MOVE", s, d) }
func (a *Adapter) ListPage(ctx context.Context, p string, o filesystem.ListOptions) (filesystem.Page, error) {
	n, e := filesystem.NormalizePath(p)
	if e != nil {
		return filesystem.Page{}, e
	}
	cursor := ""
	if o.Cursor != "" {
		b, e := base64.RawURLEncoding.DecodeString(o.Cursor)
		if e != nil {
			return filesystem.Page{}, filesystem.ErrInvalidPath
		}
		cursor = string(b)
	}
	limit := o.PageSize
	if limit <= 0 {
		limit = 1000
	}
	all := []filesystem.Entry{}
	var walk func(string) error
	walk = func(dir string) error {
		items, e := a.propfind(ctx, dir, "1")
		if e != nil {
			return e
		}
		sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })
		for _, v := range items {
			if v.Path == dir {
				continue
			}
			if path.Dir(v.Path) != dir && !(dir == "" && path.Dir(v.Path) == ".") {
				return fmt.Errorf("webdav response contains unexpected path")
			}
			if v.Path > cursor {
				kind := filesystem.EntryFile
				if v.IsDir {
					kind = filesystem.EntryDirectory
				}
				all = append(all, filesystem.Entry{Path: v.Path, Type: kind, Size: v.Size, LastModified: v.LastModified})
				if len(all) > limit {
					return nil
				}
			}
			if o.Recursive && v.IsDir {
				if e = walk(v.Path); e != nil {
					return e
				}
				if len(all) > limit {
					return nil
				}
			}
		}
		return nil
	}
	if e = walk(n); e != nil {
		return filesystem.Page{}, e
	}
	out := filesystem.Page{Items: all}
	if len(all) > limit {
		out.Items = all[:limit]
		out.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(out.Items[len(out.Items)-1].Path))
	}
	return out, nil
}

// contextReader stops spooling if the caller cancels the operation.
type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
