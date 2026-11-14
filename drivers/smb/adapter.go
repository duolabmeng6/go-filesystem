package smb

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	smb2 "github.com/cloudsoda/go-smb2"
	"github.com/duolabmeng6/go-filesystem"
)

type Config struct {
	Host     string
	Port     int
	Share    string
	Username string
	Password string
	Domain   string
	Root     string
}
type Adapter struct {
	conn      net.Conn
	session   *smb2.Session
	share     *smb2.Share
	root      string
	closeOnce sync.Once
	closeErr  error
}

func NewFactory() filesystem.DriverFactory {
	return func(ctx context.Context, d filesystem.DiskConfig) (filesystem.Adapter, error) {
		str := func(k string) string { v, _ := d.Options[k].(string); return v }
		port, err := portOption(d.Options["port"])
		if err != nil {
			return nil, err
		}
		return New(ctx, Config{Host: str("host"), Port: port, Share: str("share"), Username: str("username"), Password: str("password"), Domain: str("domain"), Root: d.Root})
	}
}
func portOption(value any) (int, error) {
	if value == nil {
		return 0, nil
	}
	var port int64
	var err error
	switch n := value.(type) {
	case int:
		port = int64(n)
	case int64:
		port = n
	case float64:
		if n != math.Trunc(n) || n < 1 || n > 65535 {
			return 0, fmt.Errorf("%w: SMB 端口不合法", filesystem.ErrInvalidPath)
		}
		port = int64(n)
	case json.Number:
		port, err = n.Int64()
	case string:
		port, err = strconv.ParseInt(n, 10, 32)
	default:
		return 0, fmt.Errorf("%w: SMB 端口不合法", filesystem.ErrInvalidPath)
	}
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("%w: SMB 端口不合法", filesystem.ErrInvalidPath)
	}
	return int(port), nil
}

func New(ctx context.Context, c Config) (*Adapter, error) {
	c.Host = strings.TrimSpace(c.Host)
	c.Share = strings.TrimSpace(c.Share)
	c.Username = strings.TrimSpace(c.Username)
	c.Domain = strings.TrimSpace(c.Domain)
	if c.Port == 0 {
		c.Port = 445
	}
	if c.Host == "" || c.Share == "" || c.Username == "" || c.Port < 1 || c.Port > 65535 || strings.ContainsAny(c.Host, "/\\\x00") || strings.ContainsAny(c.Share, "/\\\x00") {
		return nil, fmt.Errorf("%w: SMB 服务器、共享名、用户名或端口不合法", filesystem.ErrInvalidPath)
	}
	root, err := filesystem.NormalizePath(strings.Trim(strings.TrimSpace(c.Root), "/"))
	if err != nil {
		return nil, err
	}
	setupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(setupCtx, "tcp", net.JoinHostPort(c.Host, strconv.Itoa(c.Port)))
	if err != nil {
		return nil, fmt.Errorf("smb connect: %w", err)
	}
	dialer := &smb2.Dialer{Initiator: &smb2.NTLMInitiator{User: c.Username, Password: c.Password, Domain: c.Domain}}
	session, err := dialer.DialConn(setupCtx, conn, c.Host)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("smb authentication: %w", err)
	}
	share, err := session.WithContext(setupCtx).Mount(c.Share)
	if err != nil {
		_ = session.Logoff()
		_ = conn.Close()
		return nil, fmt.Errorf("smb share: %w", err)
	}
	a := &Adapter{conn: conn, session: session, share: share, root: root}
	if root != "" {
		if err = a.check(setupCtx, root, false); err != nil {
			_ = a.Close()
			return nil, err
		}
		info, e := share.WithContext(setupCtx).Lstat(root)
		if e != nil || !info.IsDir() {
			_ = a.Close()
			if e != nil {
				return nil, mapError(e)
			}
			return nil, fmt.Errorf("%w: SMB 根目录不是文件夹", filesystem.ErrInvalidPath)
		}
	}
	return a, nil
}
func (a *Adapter) Close() error {
	a.closeOnce.Do(func() {
		_ = a.conn.SetDeadline(time.Now().Add(2 * time.Second))
		a.closeErr = a.share.Umount()
		if e := a.session.Logoff(); a.closeErr == nil {
			a.closeErr = e
		}
		if e := a.conn.Close(); a.closeErr == nil {
			a.closeErr = e
		}
	})
	return a.closeErr
}
func (a *Adapter) Capabilities() filesystem.CapabilitySet {
	return filesystem.NewCapabilitySet(filesystem.CapabilityCopy, filesystem.CapabilityMove, filesystem.CapabilityDirectory)
}
func (a *Adapter) DirectorySemantics() filesystem.DirectorySemantics { return filesystem.DirectoryReal }
func (a *Adapter) target(p string) (string, error) {
	n, e := filesystem.NormalizePath(p)
	if e != nil {
		return "", e
	}
	if n == "" {
		return a.root, nil
	}
	if a.root == "" {
		return n, nil
	}
	return a.root + "/" + n, nil
}

// Reject links in every existing component so server-side links cannot escape the configured root.
func (a *Adapter) check(ctx context.Context, p string, missing bool) error {
	if p == "" {
		return nil
	}
	cur := ""
	for _, part := range strings.Split(p, "/") {
		if cur == "" {
			cur = part
		} else {
			cur += "/" + part
		}
		info, e := a.share.WithContext(ctx).Lstat(cur)
		if e != nil {
			if missing && errors.Is(e, os.ErrNotExist) {
				return nil
			}
			return mapError(e)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: 不支持符号链接", filesystem.ErrInvalidPath)
		}
	}
	return nil
}
func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return filesystem.ErrNotFound
	}
	if errors.Is(err, os.ErrExist) {
		return filesystem.ErrAlreadyExists
	}
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("%w: %v", filesystem.ErrReadOnly, err)
	}
	return err
}
func (a *Adapter) existing(ctx context.Context, p string) (string, error) {
	t, e := a.target(p)
	if e != nil {
		return "", e
	}
	return t, a.check(ctx, t, false)
}
func (a *Adapter) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	t, e := a.existing(ctx, p)
	if e != nil {
		return nil, e
	}
	info, e := a.share.WithContext(ctx).Lstat(t)
	if e != nil {
		return nil, mapError(e)
	}
	if info.IsDir() {
		return nil, filesystem.ErrUnsupported
	}
	f, e := a.share.WithContext(ctx).Open(t)
	return f, mapError(e)
}
func (a *Adapter) Stat(ctx context.Context, p string) (filesystem.FileInfo, error) {
	t, e := a.existing(ctx, p)
	if e != nil {
		return filesystem.FileInfo{}, e
	}
	info, e := a.share.WithContext(ctx).Lstat(t)
	if e != nil {
		return filesystem.FileInfo{}, mapError(e)
	}
	return filesystem.FileInfo{Path: p, Size: info.Size(), LastModified: info.ModTime(), IsDir: info.IsDir()}, nil
}
func (a *Adapter) Exists(ctx context.Context, p string) (bool, error) {
	_, e := a.existing(ctx, p)
	if errors.Is(e, filesystem.ErrNotFound) {
		return false, nil
	}
	return e == nil, e
}
func (a *Adapter) parent(ctx context.Context, t string) error {
	dir := path.Dir(t)
	if dir == "." {
		return nil
	}
	if e := a.check(ctx, dir, true); e != nil {
		return e
	}
	if e := a.share.WithContext(ctx).MkdirAll(dir, 0755); e != nil {
		return mapError(e)
	}
	return a.check(ctx, dir, false)
}
func (a *Adapter) Write(ctx context.Context, p string, r io.Reader, o filesystem.WriteOptions) error {
	if r == nil {
		return filesystem.ErrInvalidPath
	}
	if !o.Visibility.Valid() {
		return filesystem.ErrInvalidVisibility
	}
	t, e := a.target(p)
	if e != nil {
		return e
	}
	if t == a.root {
		return filesystem.ErrInvalidPath
	}
	if e = a.parent(ctx, t); e != nil {
		return e
	}
	if e = a.check(ctx, t, true); e != nil {
		return e
	}
	share := a.share.WithContext(ctx)
	if !o.Overwrite {
		if _, e = share.Lstat(t); e == nil {
			return filesystem.ErrAlreadyExists
		} else if !errors.Is(e, os.ErrNotExist) {
			return mapError(e)
		}
	}
	// The SMB client renames with replacement; exclusive creation avoids overwriting a concurrent new file.
	dest := t
	if o.AtomicWrite && o.Overwrite {
		var b [16]byte
		if _, e = rand.Read(b[:]); e != nil {
			return e
		}
		dest = path.Join(path.Dir(t), ".afile-"+hex.EncodeToString(b[:])+".tmp")
		defer func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = a.share.WithContext(cleanupCtx).Remove(dest)
		}()
	}
	flags := os.O_WRONLY | os.O_CREATE
	if o.Overwrite && !o.AtomicWrite {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
	}
	f, e := share.OpenFile(dest, flags, 0600)
	if e != nil {
		return mapError(e)
	}
	_, e = io.Copy(f, &contextReader{ctx: ctx, reader: r})
	closeErr := f.Close()
	if e != nil || closeErr != nil {
		if !o.Overwrite {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = a.share.WithContext(cleanupCtx).Remove(dest)
			cancel()
		}
		if e != nil {
			return mapError(e)
		}
		return mapError(closeErr)
	}
	if o.AtomicWrite && o.Overwrite {
		return mapError(share.Rename(dest, t))
	}
	return nil
}
func (a *Adapter) Delete(ctx context.Context, p string) error {
	t, e := a.existing(ctx, p)
	if e != nil {
		return e
	}
	if t == a.root {
		return filesystem.ErrInvalidPath
	}
	info, e := a.share.WithContext(ctx).Lstat(t)
	if e != nil {
		return mapError(e)
	}
	if info.IsDir() {
		return filesystem.ErrUnsupported
	}
	return mapError(a.share.WithContext(ctx).Remove(t))
}
func (a *Adapter) MakeDirectory(ctx context.Context, p string, o filesystem.DirectoryOptions) error {
	if !o.Visibility.Valid() {
		return filesystem.ErrInvalidVisibility
	}
	t, e := a.target(p)
	if e != nil {
		return e
	}
	if t == a.root {
		return nil
	}
	if e = a.check(ctx, t, true); e != nil {
		return e
	}
	if e = a.share.WithContext(ctx).MkdirAll(t, 0755); e != nil {
		return mapError(e)
	}
	return a.check(ctx, t, false)
}
func (a *Adapter) DeleteDirectory(ctx context.Context, p string) error {
	t, e := a.existing(ctx, p)
	if e != nil {
		return e
	}
	if t == a.root {
		return filesystem.ErrInvalidPath
	}
	i, e := a.share.WithContext(ctx).Lstat(t)
	if e != nil {
		return mapError(e)
	}
	if !i.IsDir() {
		return filesystem.ErrInvalidPath
	}
	return mapError(a.share.WithContext(ctx).RemoveAll(t))
}
func (a *Adapter) Copy(ctx context.Context, src, dst string) error {
	r, e := a.Open(ctx, src)
	if e != nil {
		return e
	}
	defer r.Close()
	return a.Write(ctx, dst, r, filesystem.DefaultWriteOptions())
}
func (a *Adapter) Move(ctx context.Context, src, dst string) error {
	s, e := a.existing(ctx, src)
	if e != nil {
		return e
	}
	t, e := a.target(dst)
	if e != nil {
		return e
	}
	if s == a.root || t == a.root {
		return filesystem.ErrInvalidPath
	}
	if e = a.parent(ctx, t); e != nil {
		return e
	}
	if e = a.check(ctx, t, true); e != nil {
		return e
	}
	return mapError(a.share.WithContext(ctx).Rename(s, t))
}
func (a *Adapter) ListPage(ctx context.Context, p string, o filesystem.ListOptions) (filesystem.Page, error) {
	t, e := a.existing(ctx, p)
	if e != nil {
		return filesystem.Page{}, e
	}
	limit := o.PageSize
	if limit <= 0 {
		limit = 1000
	}
	cursor := ""
	if o.Cursor != "" {
		raw, e := base64.RawURLEncoding.DecodeString(o.Cursor)
		if e != nil {
			return filesystem.Page{}, filesystem.ErrInvalidPath
		}
		cursor = string(raw)
	}
	items := make([]filesystem.Entry, 0, limit+1)
	var walk func(string, string) error
	walk = func(dir, prefix string) error {
		list, e := a.share.WithContext(ctx).ReadDir(dir)
		if e != nil {
			return mapError(e)
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Name() < list[j].Name() })
		for _, v := range list {
			if e = ctx.Err(); e != nil {
				return e
			}
			if v.Mode()&os.ModeSymlink != 0 {
				continue
			}
			name := v.Name()
			if prefix != "" {
				name = prefix + "/" + name
			}
			if cursor == "" || name > cursor {
				kind := filesystem.EntryFile
				if v.IsDir() {
					kind = filesystem.EntryDirectory
				}
				items = append(items, filesystem.Entry{Path: name, Type: kind, Size: v.Size(), LastModified: v.ModTime()})
				if len(items) > limit {
					return nil
				}
			}
			if o.Recursive && v.IsDir() {
				if e = walk(path.Join(dir, v.Name()), name); e != nil {
					return e
				}
				if len(items) > limit {
					return nil
				}
			}
		}
		return nil
	}
	if e = walk(t, p); e != nil {
		return filesystem.Page{}, e
	}
	page := filesystem.Page{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(page.Items[len(page.Items)-1].Path))
	}
	return page, nil
}

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
