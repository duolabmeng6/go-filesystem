# go-filesystem 开发方案

## 1. 目标

`go-filesystem` 是一个 Go 文件存储抽象库，包名为 `filesystem`。它参考 Laravel Filesystem / Storage 的核心概念，但不复制 Laravel 的 PHP API，也不绑定 Flysystem。

核心目标：

- 使用 Go 风格的接口、错误处理、`context.Context` 和流式 I/O。
- 提供 `disk`、`driver`、`default disk`、`local disk`、`public disk`、`visibility`、`URL`、`temporary URL`、`fake` 等概念。
- 第一阶段只实现核心包、`local` driver 和类似 `Storage::fake()` 的测试能力。
- 后续可扩展 S3、SFTP、OSS、COS、Qiniu 等 driver，但核心包不强制依赖任何云厂商 SDK。
- 所有代码标识符、包名、文件名、配置键、测试名使用英文，不再使用中文命名。

参考来源：

- Laravel 13.x File Storage 文档：https://laravel.com/docs/13.x/filesystem
- 本项目参考实现：`Reference/efile`

## 2. Laravel 命名参考与 Go 化取舍

保留 Laravel 的概念命名：

- `disk`：一个已配置好的存储实例，例如 `local`、`public`、`s3`。
- `driver`：创建 disk 的底层适配器类型，例如 `local`、`s3`、`oss`。
- `default disk`：未显式选择 disk 时使用的默认 disk。
- `local`：本地文件系统 driver。
- `public`：通常是一个 disk 名称，默认 `VisibilityPublic`，并配置 `BaseURL`。
- `visibility`：跨 driver 的可见性抽象，只提供 `public` / `private`。
- `URL` / `TemporaryURL`：普通访问 URL 和临时访问 URL。
- `fake` / `persistentFake`：测试时替换某个 disk。
- `extend`：注册自定义 driver factory。

Go 化取舍：

- 不做全局 facade。核心 API 使用显式的 `Manager` 和 `Disk` 对象。
- 不使用 `interface{}` 作为写入数据入口。提供 `Put(ctx, path, []byte, ...)` 和 `Write(ctx, path, io.Reader, ...)`。
- 不用 `panic` 表示配置错误。构造和查找都返回 `error`。
- 不强制所有 driver 都支持所有能力。URL、temporary URL、visibility 等能力可通过可选接口实现，不支持时返回 `ErrUnsupported`。
- 不使用中文方法名或字段名；参考实现里的 `文件储存类`、`本地文件储存器` 等都改为英文命名。

## 3. 对 Reference/efile 的吸收与重写

参考实现中值得保留的思想：

- 一个 manager 持有多个 disk，并有当前默认 disk。
- 各 driver 暴露相同的文件操作能力：`Put`、`Get`、`Delete`、`Move`、`Copy`、`Exists`、`Size`、`List`、`MimeType`。
- 云 driver 有 `PathPrefix`、`Domain`、`Private` 等概念，可映射为后续 driver 配置。
- 测试里已有跨 driver 行为验证的雏形，可升级成 driver contract tests。

必须重写的点：

- 删除所有中文标识符。
- 删除核心包对 `goefun/ecore`、OSS SDK、Qiniu SDK 的直接依赖。
- 禁止 `root + key` 字符串拼接，改成统一 path normalizer 和 root escape 检查。
- 所有公开 I/O 方法都接收 `context.Context`。
- `Get` 只是便捷方法，核心读写必须支持 `io.Reader` / `io.ReadCloser`。
- `getDisk()` 不得 `panic`，改成返回 `ErrDiskNotFound`。
- 本地 driver 不需要全局大锁；依赖 `os` 原子语义和必要的目录创建即可。
- `List` 拆分为 `Files`、`AllFiles`、`Directories`、`AllDirectories`，与 Laravel 命名接近且语义更清晰。

## 4. 包结构

第一阶段建议结构：

```text
go-filesystem/
  go.mod
  disk.go
  manager.go
  config.go
  errors.go
  path.go
  visibility.go
  metadata.go
  url.go
  decorators.go
  fake.go
  options.go
  local/
    config.go
    adapter.go
    factory.go
  filesystemtest/
    contract.go
    assertions.go
  README.md
```

后续 driver 建议放在独立子包或独立 module：

```text
drivers/s3/
drivers/sftp/
drivers/oss/
drivers/cos/
drivers/qiniu/
```

核心包 `filesystem` 只依赖 Go 标准库。`local` 作为第一阶段内置 driver 子包，也只依赖标准库和根包类型。云 driver 子包可以依赖对应 SDK，并通过 `Manager.Extend` 注册。

包依赖约定：

- 根包 `filesystem` 不导入 `filesystem/local`，避免 import cycle。
- `local` 子包导入根包，提供 typed config、adapter 和 factory。
- 使用配置构造 `Manager` 前，应用需要显式注册 local factory，或使用项目提供的 helper 包。

示例：

```go
m := filesystem.New()
m.MustExtend("local", local.NewFactory())
```

## 5. 核心类型设计

### 5.1 Manager

`Manager` 负责配置、driver factory、disk 实例缓存和默认 disk 分发。`Manager` 必须是 goroutine-safe，内部使用锁保护 default disk、disk cache 和 driver registry。

并发约定：

- `Disk`、`DefaultDisk`、default disk 代理方法可以在多个 goroutine 中并发调用。
- `SetDefaultDisk`、`RegisterDisk`、`Extend` 也必须加锁，但建议只在初始化或测试 fake 阶段调用。
- `Extend` 对已存在 driver 名称返回 `ErrDriverAlreadyRegistered`，除非后续显式加入 override option。
- `RegisterDisk` 只用于注册不存在的 disk；`Fake` 替换已有 disk 时通过 `ReplaceDisk` 完成，避免绕过 Manager 锁。
- 不得持锁调用 `DriverFactory`；factory 可能做 I/O 或间接回调 manager，必须先取出配置和 factory，再释放锁后构建。
- `ReplaceDisk` 不影响已经被 goroutine 持有的旧 `*Disk`；后续 `Disk(name)` 返回新实例。
- `MustExtend` 只用于 init/test 场景，是对“配置错误不 panic”的有意例外。

主要方法：

```go
type Manager struct {}

func New(opts ...ManagerOption) *Manager
func NewFromConfig(ctx context.Context, config Config, opts ...ManagerOption) (*Manager, error)
func (m *Manager) Disk(name string) (*Disk, error)
func (m *Manager) DefaultDisk() (*Disk, error)
func (m *Manager) SetDefaultDisk(name string) error
func (m *Manager) RegisterDisk(name string, disk *Disk) error
func (m *Manager) ReplaceDisk(name string, disk *Disk) error
func (m *Manager) Extend(driver string, factory DriverFactory) error
func (m *Manager) MustExtend(driver string, factory DriverFactory)
func (m *Manager) Build(ctx context.Context, config DiskConfig) (*Disk, error)
```

便捷代理方法可放在 `Manager` 上，转发到 default disk：

```go
func (m *Manager) Put(ctx context.Context, path string, data []byte, opts ...WriteOption) error
func (m *Manager) Get(ctx context.Context, path string) ([]byte, error)
func (m *Manager) Exists(ctx context.Context, path string) (bool, error)
```

### 5.2 Disk

`Disk` 是用户主要操作对象。它包装底层 `Adapter`，统一处理 path normalization、错误包装、fallback 行为和可选能力检测。

核心方法：

```go
func (d *Disk) Put(ctx context.Context, path string, data []byte, opts ...WriteOption) error
func (d *Disk) Write(ctx context.Context, path string, r io.Reader, opts ...WriteOption) error
func (d *Disk) Get(ctx context.Context, path string) ([]byte, error)
func (d *Disk) Open(ctx context.Context, path string) (io.ReadCloser, error)
func (d *Disk) Delete(ctx context.Context, path string) error
func (d *Disk) DeleteIfExists(ctx context.Context, path string) error
func (d *Disk) DeleteMany(ctx context.Context, paths []string, opts ...DeleteOption) error
func (d *Disk) Exists(ctx context.Context, path string) (bool, error)
func (d *Disk) Missing(ctx context.Context, path string) (bool, error)
func (d *Disk) Copy(ctx context.Context, src string, dst string) error
func (d *Disk) Move(ctx context.Context, src string, dst string) error
func (d *Disk) Stat(ctx context.Context, path string) (FileInfo, error)
func (d *Disk) Size(ctx context.Context, path string) (int64, error)
func (d *Disk) LastModified(ctx context.Context, path string) (time.Time, error)
func (d *Disk) MimeType(ctx context.Context, path string) (string, error)
```

目录方法：

```go
func (d *Disk) Files(ctx context.Context, dir string) ([]string, error)
func (d *Disk) AllFiles(ctx context.Context, dir string) ([]string, error)
func (d *Disk) Directories(ctx context.Context, dir string) ([]string, error)
func (d *Disk) AllDirectories(ctx context.Context, dir string) ([]string, error)
func (d *Disk) MakeDirectory(ctx context.Context, dir string, opts ...DirectoryOption) error
func (d *Disk) DeleteDirectory(ctx context.Context, dir string) error
```

分页 list 方法：

```go
func (d *Disk) List(ctx context.Context, prefix string, opts ...ListOption) (EntryIterator, error)
func (d *Disk) ListPage(ctx context.Context, prefix string, opts ...ListOption) (Page, error)
```

`Files`、`AllFiles`、`Directories`、`AllDirectories` 是面向 Laravel 命名习惯的便捷 wrapper。底层以 `List` / `ListPage` 为主，避免云存储大目录一次性加载全部对象。

`DeleteMany` 的语义：

- 按传入顺序逐个删除。
- 尽力删除所有 path，不在第一个错误处停止。
- 返回 `MultiError`，其中记录每个失败 path 的错误。
- 文件不存在默认算错误，记录为 `ErrNotFound`。
- 单文件删除优先使用 `Delete`，第一阶段业务示例不鼓励直接使用批量删除。

删除语义：

- `Delete(path)`：文件不存在返回 `ErrNotFound`。
- `DeleteIfExists(path)`：文件不存在返回 `nil`。
- `DeleteMany(paths)`：每个 path 独立记录错误；不存在默认算错误。
- `DeleteMany(paths, WithIgnoreMissing())`：cleanup 场景使用；文件不存在不记为错误。

URL 和 visibility 方法：

```go
func (d *Disk) URL(ctx context.Context, path string) (string, error)
func (d *Disk) TemporaryURL(ctx context.Context, path string, expiresAt time.Time, opts ...URLOption) (string, error)
func (d *Disk) GetVisibility(ctx context.Context, path string) (Visibility, error)
func (d *Disk) SetVisibility(ctx context.Context, path string, visibility Visibility) error
```

### 5.3 Adapter 与可选能力

`Adapter` 是 driver contract 的核心。`Disk` 会在调用 adapter 前完成 path 检查，因此 adapter 收到的是标准化后的相对路径。

必选 `Adapter` 只包含真正跨 local 和对象存储都自然成立的能力：

```go
type Adapter interface {
    Write(ctx context.Context, path string, r io.Reader, opts WriteOptions) error
    Open(ctx context.Context, path string) (io.ReadCloser, error)
    Delete(ctx context.Context, path string) error
    Exists(ctx context.Context, path string) (bool, error)
    Stat(ctx context.Context, path string) (FileInfo, error)
    ListPage(ctx context.Context, prefix string, opts ListOptions) (Page, error)
    Capabilities() CapabilitySet
}
```

能力声明：

```go
type Capability string

const (
    CapabilityCopy         Capability = "copy"
    CapabilityMove         Capability = "move"
    CapabilityDirectory    Capability = "directory"
    CapabilityURL          Capability = "url"
    CapabilityTemporaryURL Capability = "temporary_url"
    CapabilityVisibility   Capability = "visibility"
)

type CapabilitySet map[Capability]struct{}

func (s CapabilitySet) Has(cap Capability) bool
```

可选能力接口仍保留为调用合约，但 `Disk` 判断支持能力时必须先看 `Capabilities()`，不能只靠 type assertion。这样 decorator 即使拥有某个方法，也不会因为 Go method set 误导能力检测。

```go
type Copier interface {
    Copy(ctx context.Context, src string, dst string) error
}

type Mover interface {
    Move(ctx context.Context, src string, dst string) error
}

type DirectoryManager interface {
    MakeDirectory(ctx context.Context, dir string, opts DirectoryOptions) error
    DeleteDirectory(ctx context.Context, dir string) error
}

type DirectorySemantics string

const (
    DirectoryReal        DirectorySemantics = "real"
    DirectoryPrefixOnly  DirectorySemantics = "prefix_only"
    DirectoryUnsupported DirectorySemantics = "unsupported"
)

type DirectorySemanticsProvider interface {
    DirectorySemantics() DirectorySemantics
}

type URLGenerator interface {
    URL(ctx context.Context, path string) (string, error)
}

type TemporaryURLGenerator interface {
    TemporaryURL(ctx context.Context, path string, expiresAt time.Time, opts URLOptions) (string, error)
}

type VisibilityController interface {
    GetVisibility(ctx context.Context, path string) (Visibility, error)
    SetVisibility(ctx context.Context, path string, visibility Visibility) error
}
```

adapter decorator：

```go
func Scoped(adapter Adapter, prefix string) (Adapter, error)
func ReadOnly(adapter Adapter) Adapter
```

decorator 构建顺序：

```text
base adapter
 -> scoped adapter
 -> read-only adapter
 -> disk wrapper
```

规则：

- `Scoped` 负责统一 path prefix 行为。
- `ReadOnly` 拦截 `Write`、`Delete`、`DeleteDirectory`、`SetVisibility` 等所有写类操作并返回 `ErrReadOnly`。
- `Disk` 负责 path normalization、错误包装、fallback 行为和 capability registry 检测，不直接承载 read-only 策略。
- decorator 必须转发 capability registry，不能因为包装而丢失能力。
- `Scoped` 包装后必须保留底层 capability set，并在对应能力调用时正确加/剥 prefix。
- `ReadOnly` 必须保留读能力，例如 `URL`、`TemporaryURL`、`GetVisibility`；写能力从 capability set 移除，同时写类方法仍由 wrapper 拦截并返回 `ErrReadOnly`。
- decorator 必须有 capability forwarding contract：包装前支持什么，包装后 registry 仍准确反映什么，除 read-only 明确拒绝写能力。

`Disk` 层 fallback 规则：

- adapter 未实现 `Copier` 时，`Copy` 使用 `Open` + `Write` 流式复制。
- adapter 未实现 `Mover` 时，`Move` 使用 `Copy` + `Delete`；如果 `Copy` 成功但 `Delete` 失败，返回包含 `ErrPartialFailure` 的错误，并明确表示 src 和 dst 可能同时存在。
- adapter 实现 `DirectoryManager` 时，`MakeDirectory` / `DeleteDirectory` 调用 adapter。
- adapter 未实现 `DirectoryManager` 但声明 `DirectoryPrefixOnly` 时，`MakeDirectory` 可以 no-op；否则返回 `ErrUnsupported`。
- adapter 未实现 `DirectoryManager` 时，`DeleteDirectory` 可通过 prefix list + delete 实现，或返回 `ErrUnsupported`；不得默认假装真实目录已删除。
- `Disk.ListPage` 直接调用 adapter 的 `ListPage`；`Disk.List` 基于 `ListPage` 包装 iterator。
- adapter capability registry 未声明 `CapabilityURL`、`CapabilityTemporaryURL`、`CapabilityVisibility` 时，对应方法返回 `ErrUnsupported`。

### 5.4 List 模型

```go
type Entry struct {
    Path         string
    Type         EntryType
    Size         int64
    LastModified time.Time
}

type EntryType string

const (
    EntryFile      EntryType = "file"
    EntryDirectory EntryType = "directory"
)

type ListOptions struct {
    Recursive bool
    PageSize  int
    Cursor    string
}

type Page struct {
    Items      []Entry
    NextCursor string
}

type EntryIterator interface {
    Next(ctx context.Context) (Entry, error)
    Close() error
}
```

迭代规则：

- `EntryIterator.Next(ctx)` 在遍历结束时返回 `io.EOF`。
- 调用方应使用 `errors.Is(err, io.EOF)` 判断结束。
- `Close` 用于释放底层分页请求、文件句柄或缓冲资源；内存 iterator 可以 no-op。

分页规则：

- `PageSize <= 0` 时使用默认值，例如 `1000`。
- `Cursor` 是 opaque string，调用方不能解析。
- `ListPage` 在目录未变化且单目录可完整读取的前提下，返回值按 normalized path 字典序稳定排序。
- local driver 第一阶段不扫描整棵树后再分页。
- local 单目录排序可以使用 `os.ReadDir`，文档需说明超大单目录会有内存成本。
- local cursor 是 opaque、短期有效，不保证目录变更后的强一致。
- 云 driver 必须使用服务端 page token / continuation token，不允许内部全量拉取后分页。
- `Files` 等便捷方法内部消费 iterator 并返回 `[]string`，适合中小目录；大目录应使用 `ListPage` 或 `List`。

## 6. 配置设计

```go
type Config struct {
    Default string
    Disks   map[string]DiskConfig
}

type DiskConfig struct {
    Driver     string
    Root       string
    BaseURL    string
    Visibility Visibility
    PathPrefix string
    ReadOnly   bool
    Options    map[string]any
}

type DriverFactory func(ctx context.Context, config DiskConfig) (Adapter, error)
```

典型配置：

```go
cfg := filesystem.Config{
    Default: "local",
    Disks: map[string]filesystem.DiskConfig{
        "local": {
            Driver: "local",
            Root:   "storage/app/private",
        },
        "public": {
            Driver:     "local",
            Root:       "storage/app/public",
            BaseURL:    "/storage",
            Visibility: filesystem.VisibilityPublic,
        },
    },
}
```

配置原则：

- `DiskConfig` 只承载跨 driver 的通用字段。
- `Options map[string]any` 仅用于从配置文件装载未知 driver 的场景，不作为推荐的 Go 代码入口。
- 内置 driver 不应优先从 `Options` 读取核心字段；核心字段必须提升为 typed config 或 `DiskConfig` 顶层字段。
- 云 driver 的 `Options` 只负责从 YAML、JSON、env 等外部配置转换到 typed config，不作为主要编程接口。
- 每个 driver 应提供 typed config 和 factory，例如 `local.New(local.Config{})`、`s3.New(s3.Config{})`。
- `Serve` 不进入第一阶段配置；local driver 只负责 URL 字符串生成，不负责 HTTP server。

`PathPrefix` 统一通过 `Scoped` decorator 实现，而不是让每个 driver 自行拼接。`Scoped` 负责 normalize prefix，并在所有 path 进入底层 adapter 前加前缀，在 list 结果返回前剥离前缀。这样 local、S3、OSS、COS、Qiniu 的 prefix 行为保持一致。

driver typed config 示例：

```go
type S3Config struct {
    Bucket          string
    Region          string
    Endpoint        string
    AccessKeyID     string
    AccessKeySecret string
    PathPrefix      string
    BaseURL         string
    Visibility      filesystem.Visibility
}
```

`FileInfo` 第一阶段保持克制，不承诺昂贵元数据：

```go
type FileInfo struct {
    Path         string
    Size         int64
    LastModified time.Time
    IsDir        bool
}
```

`MIMEType`、`ETag`、`Visibility` 不放入必选 `FileInfo`。需要时分别通过 `MimeType`、driver-specific metadata 或 `GetVisibility` 获取，避免 `Stat` 被迫做额外网络请求。

## 7. Visibility

定义：

```go
type Visibility string

const (
    VisibilityPrivate Visibility = "private"
    VisibilityPublic  Visibility = "public"
)
```

本地 driver 默认权限映射：

- public file：`0644`
- private file：`0600`
- public directory：`0755`
- private directory：`0700`

local driver 实现规则：

- `Write` 创建文件时不能只依赖 `os.OpenFile` 的 mode，因为最终权限会受 process `umask` 影响。
- `Write` 完成后应按默认 visibility 或 `WithVisibility(...)` 显式 `chmod`。
- `SetVisibility` 必须显式 `chmod`。
- 单次写入可以用 `WithVisibility(VisibilityPublic)` 或 `WithVisibility(VisibilityPrivate)` 覆盖 disk 默认 visibility。
- `WithVisibility(...)` 对文件写入影响文件权限；写入过程中自动创建的父目录使用 directory visibility 映射。
- `MakeDirectory(..., WithVisibility(...))` 使用 directory visibility 映射。
- `GetVisibility(path)` 对文件和目录都可支持；local driver 通过 mode 判断并结合 `FileInfo.IsDir` 选择 file/dir 映射。

local typed config 应提供权限策略：

```go
type Permissions struct {
    FilePublic  fs.FileMode
    FilePrivate fs.FileMode
    DirPublic   fs.FileMode
    DirPrivate  fs.FileMode
}
```

后续云 driver 映射：

- S3：ACL、bucket policy 或 object metadata。
- OSS / COS：对象 ACL 或 bucket policy。
- Qiniu：bucket private/public 配置与下载 URL 逻辑。

如果 driver 无法精确读取 visibility，可以返回 `ErrUnsupported`，不得伪造结果。

## 8. URL 与 TemporaryURL

`URL(ctx, path)` 规则：

- `local` disk 只有配置了 `BaseURL` 才支持 URL。
- path 必须先 normalize，再按 URL path segment 转义。
- `public` disk 通常配置 `BaseURL: "/storage"`。
- 私有 local disk 默认不支持 URL。
- `BaseURL` 只控制 URL 字符串生成；库不负责让该 URL 可访问。
- 应用必须自行通过 HTTP server、反向代理、CDN 或框架集成暴露对应 root 目录。
- 与 Laravel local URL 不同，go-filesystem 会逐段 escape URL path segment。这是有意偏离 Laravel 的安全选择，用于避免非法 URL 和 path injection。

`TemporaryURL(ctx, path, expiresAt, opts...)` 规则：

- 第一阶段只定义接口和错误语义。
- local driver 仅在配置了签名 URL builder 时支持；否则返回 `ErrUnsupported`。
- 后续 S3 / OSS / COS / Qiniu driver 可使用 SDK 生成 signed URL。
- 过期时间必须晚于当前时间，否则返回 `ErrInvalidExpiration`。

local temporary URL builder：

```go
type TemporaryURLBuilder func(ctx context.Context, path string, expiresAt time.Time, opts URLOptions) (string, error)
```

local typed config 可包含 `TemporaryURLBuilder`。未配置 builder 时，local `TemporaryURL` 返回 `ErrUnsupported`；已配置 builder 时，第一阶段必须调用 builder 生成临时 URL。库仍不提供 HTTP serving。

暂不在第一阶段实现 `TemporaryUploadURL`，但保留命名和扩展接口，后续 S3/local serve 场景再加入。

## 9. 路径安全

所有用户输入 path 都按对象存储风格处理：使用 `/` 分隔的相对路径，不接受 OS 原生绝对路径。

`NormalizePath` 规则：

- 允许空 path 只用于 list root。
- 拒绝绝对路径，例如 `/etc/passwd`、`C:\Windows`。
- 拒绝 `..`、`.`、空 segment、NUL byte、控制字符。
- 将 `\` 视为非法字符，而不是自动转换。
- 拒绝 Windows drive path、UNC path 和带 colon 的 segment，例如 `C:\Windows`、`C:/Windows`、`\\server\share`、`a:b`。
- Windows 保留名按安全默认值拒绝，例如 `CON`、`NUL`、`AUX`、`PRN`、`COM1`、`LPT1`，包括带扩展名形式。
- 使用 `path.Clean` 处理对象 key，不使用 `filepath.Clean` 直接清洗用户输入。
- 返回标准化后的 slash path，例如 `avatars/user.png`。

local driver root escape 防护：

- root 在初始化时转为 absolute path。
- root 初始化时建议允许 root 本身是部署层面的 symlink，但必须 `EvalSymlinks` 后保存 resolved root。
- 用户 path normalize 后再转换为本地 path。
- 使用 `filepath.Rel(root, target)` 做基础 root containment 检查，但不能把它当作 symlink 防护。
- 默认安全模式拒绝 path 任一已存在 segment 是 symlink；读、写、删、列目录都要用 `Lstat` 逐段检查。
- 写入前创建父目录后，再次逐段 `Lstat` 检查父目录没有 symlink escape。
- 第一阶段不提供允许 symlink 的配置开关；如后续需要，必须单独设计并在文档中标明风险。
- `DeleteDirectory` 不允许删除 root 本身，只允许删除 root 下的子目录。
- contract tests 必须包含 symlink 指向 root 外部目录或文件的 escape 场景。
- P0 symlink 防护是应用级 best effort，不承诺作为强沙箱。
- Unix 平台后续可考虑 hardened local driver，使用 `openat`、`O_NOFOLLOW` 或 `openat2 RESOLVE_BENEATH` 缩小 TOCTOU 窗口。

local write 原子性：

- local `Write` 默认 atomic。
- 写入流程：在目标同目录创建临时文件，流式写入，按 visibility 显式 `chmod`，成功后 `rename` 到目标路径。
- 失败时尽力清理 temp file。
- `WithAtomicWrite(false)` 可关闭 atomic write，但第一阶段默认开启。
- `WithOverwrite(true)` 控制是否覆盖已存在文件；默认覆盖策略必须在 README 中明确。
- Atomic write 只承诺进程级失败不暴露半文件；不承诺断电级 durable write。后续可增加 `WithFsync()`。

context 语义：

- 所有公开方法入口先检查 `ctx.Err()`。
- local driver 的长时间流式复制在循环中周期性检查 context。
- local 文件系统底层系统调用无法保证被 context 立即中断，文档必须说明这是 best-effort。
- 云 driver 必须把 `context.Context` 传给 SDK 或 HTTP 请求。

URL 安全：

- URL path 逐段 `PathEscape`。
- 不允许把未清洗 path 直接拼接到 URL。

## 10. 错误处理

定义可用 `errors.Is` 判断的 sentinel errors：

```go
var (
    ErrDiskNotFound       = errors.New("disk not found")
    ErrDriverNotFound     = errors.New("driver not found")
    ErrDriverAlreadyRegistered = errors.New("driver already registered")
    ErrInvalidPath        = errors.New("invalid path")
    ErrNotFound           = errors.New("file not found")
    ErrAlreadyExists      = errors.New("file already exists")
    ErrUnsupported        = errors.New("operation unsupported")
    ErrReadOnly           = errors.New("disk is read-only")
    ErrPartialFailure     = errors.New("partial failure")
    ErrInvalidVisibility  = errors.New("invalid visibility")
    ErrInvalidExpiration  = errors.New("invalid expiration")
)
```

统一包装：

```go
type OpError struct {
    Op      string
    Disk    string
    Path    string
    Err     error
    Partial bool
}

type MultiError struct {
    Op     string
    Errors []PathError
}

type PathError struct {
    Path string
    Err  error
}
```

规则：

- 所有公开方法返回 `error`，不使用 `panic`。
- `Exists` 在底层 `ErrNotFound` 时返回 `(false, nil)`。
- `Exists` 不能通过 `List(prefix)` 模糊推导；local 使用 `stat`，对象存储优先使用 HEAD / metadata API，避免 `foo` 和 `foo/bar` prefix 混淆。
- `Missing` 是 `Exists` 的反向便捷方法。
- local driver 把 `os.ErrNotExist` 映射为 `ErrNotFound`。
- 写操作失败直接返回 error，不提供 Laravel 的 `throw: false` 行为。
- `DeleteMany` 返回 `MultiError` 表示部分或全部失败，并保留每个 path 的错误。
- `DeleteMany(..., WithIgnoreMissing())` 忽略 `ErrNotFound`，用于 cleanup 场景。
- `Move` fallback 在 copy 成功但 delete 失败时返回包装了 `ErrPartialFailure` 的 `OpError{Partial:true}`，错误信息必须说明 src 和 dst 可能同时存在。

## 11. Local Driver 第一阶段实现范围

支持：

- `Put` / `Write` / `Get` / `Open`
- `Delete` / `DeleteIfExists` / `DeleteMany`
- `Exists` / `Missing`
- `Copy` / `Move`
- `Stat` / `Size` / `LastModified` / `MimeType`
- `ListPage` / `List`
- `Files` / `AllFiles`
- `Directories` / `AllDirectories`
- `MakeDirectory` / `DeleteDirectory`
- `GetVisibility` / `SetVisibility`
- `URL`，前提是配置 `BaseURL`
- `TemporaryURL`，前提是配置 `TemporaryURLBuilder`
- `Scoped` / `ReadOnly` decorator

暂不支持或仅预留：

- `TemporaryUploadURL`：后续阶段。
- 自动根据 MIME 生成随机文件名的 `PutFile`：后续作为便捷 API 添加。

本地 MIME 识别策略：

- 优先读取文件头并使用 `http.DetectContentType`。
- 可结合扩展名作为后续增强，但第一阶段避免复杂依赖。

## 12. Fake / PersistentFake 测试能力

目标是提供 Laravel `Storage::fake()` 的 Go 版本，但使用 `testing.TB` 和显式 manager。

建议 API：

```go
func Fake(t testing.TB, manager *Manager, name string, opts ...FakeOption) *FakeDisk
func PersistentFake(manager *Manager, name string, root string, opts ...FakeOption) (*FakeDisk, error)
func NewFakeDisk(t testing.TB, opts ...FakeOption) *FakeDisk
```

行为：

- `Fake` 使用 `t.TempDir()` 创建隔离 local disk，并通过 `ReplaceDisk` 替换 manager 中同名 disk。
- 创建 fake 前记录原 disk 是否存在。
- `t.Cleanup` 时清理临时目录，并恢复原 disk。
- 如果原 disk 不存在，cleanup 时删除该 fake disk 注册，避免测试污染。
- `PersistentFake` 使用指定 root，不自动删除，便于调试失败产物。
- fake disk 默认 visibility 为 `private`，可用 option 改成 `public`。

断言方法：

```go
func (f *FakeDisk) AssertExists(paths ...string)
func (f *FakeDisk) AssertMissing(paths ...string)
func (f *FakeDisk) AssertCount(dir string, expected int)
func (f *FakeDisk) AssertDirectoryEmpty(dir string)
func (f *FakeDisk) AssertContent(path string, expected []byte)
```

断言失败使用 `t.Helper()` 和 `t.Fatalf`，符合 Go 测试习惯。

## 13. Driver Contract Tests

提供 `filesystemtest` 子包，供内置 local driver 和未来云 driver 复用。

建议入口：

```go
func RunObjectContract(t *testing.T, newDisk func(t testing.TB) *filesystem.Disk)
func RunDirectoryContract(t *testing.T, newDisk func(t testing.TB) *filesystem.Disk)
func RunListContract(t *testing.T, newDisk func(t testing.TB) *filesystem.Disk)
func RunVisibilityContract(t *testing.T, newDisk func(t testing.TB) *filesystem.Disk)
func RunURLContract(t *testing.T, newDisk func(t testing.TB) *filesystem.Disk)
func RunTemporaryURLContract(t *testing.T, newDisk func(t testing.TB) *filesystem.Disk)
func RunPathSafetyContract(t *testing.T, newDisk func(t testing.TB) *filesystem.Disk)
func RunDecoratorContract(t *testing.T, newDisk func(t testing.TB) *filesystem.Disk)
```

`RunObjectContract` 覆盖：

- 写入和读取 bytes。
- 使用 `io.Reader` 流式写入大文件。
- missing / exists 行为。
- delete 不存在文件的错误语义。
- copy / move 后源文件和目标文件状态。
- size / mimeType / lastModified。
- context canceled 时尽快返回 `context.Canceled` 或包装后的等价错误。

其他 contract 覆盖：

- `RunDirectoryContract`：`MakeDirectory`、`DeleteDirectory` 和目录边界行为。
- `RunListContract`：分页 list、recursive list、`Files` / `AllFiles` / `Directories` / `AllDirectories` wrapper。
- `RunVisibilityContract`：public/private visibility 设置和读取。
- `RunURLContract`：`URL` 和 URL path escaping。
- `RunTemporaryURLContract`：临时 URL 过期时间和不支持能力语义。
- `RunPathSafetyContract`：`../x`、`/x`、`a/../../x`、NUL byte、backslash、Windows drive path、UNC path、colon segment、Windows reserved names、symlink escape。
- `RunDecoratorContract`：`Scoped` 和 `ReadOnly` 的 capability registry forwarding、prefix 加/剥、写能力拦截。
- read-only disk 单独提供 `RunReadOnlyContract`，验证写、删、移动、visibility 修改都返回 `ErrReadOnly`。

contract 分层规则：

- local driver 必须通过全部适用 contract。
- fake disk 必须通过 object、directory、list、visibility、path safety contract。
- 云 driver 至少通过 object 和 list contract；目录、visibility、URL、temporary URL 视能力选择对应 contract。
- driver 不支持的能力必须返回 `ErrUnsupported`，不能通过空实现伪装成功。
- 云 driver contract 使用环境变量配置，缺少凭证时 `t.Skip`。

## 14. 后续 Driver 扩展

扩展方式：

```go
manager.Extend("s3", s3.NewFactory())
manager.Extend("oss", oss.NewFactory())
manager.Extend("qiniu", qiniu.NewFactory())
```

设计规则：

- 云 SDK 依赖只存在于 driver 子包或独立 module。
- 面向配置文件的 driver factory 可以接收 `DiskConfig`，但 Go 代码入口优先提供 typed config。
- 所有云 driver 都必须通过适用的 `filesystemtest` 分层 contract，不能要求通过不支持能力的 contract。
- S3-compatible 服务优先复用 `s3` driver，通过 `Endpoint`、`Region`、`UsePathStyle` 等配置适配。
- OSS / COS / Qiniu 使用英文配置字段：`Bucket`、`Endpoint`、`Region`、`AccessKeyID`、`AccessKeySecret`、`PathPrefix`、`BaseURL`、`Private`。
- 云 driver 的分页 list 必须暴露 cursor/page token，不允许内部无限拉取全部对象后再返回。

## 15. 阶段计划

P0a：API 骨架可跑

1. 初始化 module、root package `filesystem` 和 `local` 子包。
2. 定义 `Manager`、`Disk`、最小 `Adapter`、capability registry、核心 option 类型。
3. 定义 `Config`、`DiskConfig`、`Visibility`、`FileInfo`、`Entry`、`Page`、`EntryIterator`。
4. 实现错误类型、`OpError`、`MultiError`、`ErrPartialFailure`。
5. 实现 `NormalizePath`、Windows path 拒绝规则和基础 table tests。
6. 实现 goroutine-safe `Manager`、`New`、`NewFromConfig`、`Disk(name)`、`Build`、`Extend`、`MustExtend`、`RegisterDisk`、`ReplaceDisk`。
7. 实现 `local` basic adapter 和 typed `local.Config`。
8. 实现 `Write` / `Open` / `Get` / `Put` / `Delete` / `DeleteIfExists` / `Exists` / `Missing` / `Stat`。

P0b：local 可生产使用

1. 实现 local atomic write、visibility、chmod、permissions config。
2. 实现 URL with `BaseURL`，并在 README 明确只生成 URL、不负责 serving。
3. 实现 `MakeDirectory` / `DeleteDirectory` 和目录 visibility。
4. 实现 `ListPage` + `EntryIterator`，local 不扫描整棵树，排序承诺按文档边界执行。
5. 实现 `Files` / `AllFiles` / `Directories` / `AllDirectories`。
6. 实现 `Copy` / `Move` fallback，并明确 partial failure。
7. 实现 local symlink best-effort 防护、root resolved path 和相关 tests。
8. 实现 `TemporaryURLBuilder` 调用链；未配置 builder 时返回 `ErrUnsupported`。

P0c：测试生态和 decorator

1. 实现 `Fake`、`PersistentFake`、fake assertions 和 fake cleanup 恢复原 disk。
2. 实现 `Scoped` adapter 和 `ReadOnly` adapter。
3. 实现 decorator capability registry forwarding contract。
4. 编写 object、directory、path safety、visibility、URL、temporary URL、decorator、read-only 的 contract tests。
5. 编写 README 示例，覆盖 default disk、named disk、public URL、temporary URL builder、fake disk、scoped disk、read-only disk、streaming write。

P1：稳定性增强

1. `WithFsync()`，提供断电级 durable write 选项。
2. driver-specific metadata，例如 ETag、storage class、checksum。
3. 更丰富的 config loader，将 YAML、JSON、env 转换到 typed config。
4. `TemporaryUploadURL` 预研。

P2：后续 driver 和便捷能力

1. `PutFile` / MIME-based filename。
2. S3 / OSS / COS / Qiniu / SFTP driver。

## 16. 非目标

第一阶段不做：

- 不实现 S3 / OSS / COS / Qiniu / SFTP driver。
- 不实现 HTTP server 或下载响应封装。
- 不实现 Laravel facade 式全局单例。
- 不实现复杂上传文件对象、图片 fake、MIME 推断生成随机文件名。
- 不引入云厂商 SDK。

## 17. Release Gates

P0a Gate：

- Manager / Disk / Adapter API reviewed and frozen for P0a scope。
- capability registry replaces type-assertion-only capability detection。
- no public API uses `interface{}` for write data。
- all public code identifiers are English。
- `NormalizePath` table tests pass。
- `NormalizePath` fuzz test covers Unicode、control chars、Windows mixed paths、URL encoded strings。
- Windows path cases covered：`C:\Windows`、`C:/Windows`、`\\server\share`、`CON`、`NUL`、`aux.txt`、`a:b`。
- `OpError`、`MultiError` implement `Unwrap()` / `Is()` so `errors.Is(err, ErrNotFound)` and `errors.Is(err, ErrPartialFailure)` work。
- Manager tests define lock behavior and prove `DriverFactory` is not called while manager lock is held。
- `ReplaceDisk` concurrent semantics tested：old holders keep old disk, future `Disk(name)` gets replacement。
- `go test ./...` passes。

P0b Gate：

- local symlink escape tests pass，并注明 P0 是 best-effort 防护，不是强沙箱。
- local root missing behavior fixed and tested：初始化创建、首次写入创建或报错三选一，并在 README 说明。
- `DeleteDirectory(root)` rejected。
- `Write` atomic by default。
- atomic write tests cover reader failure、context canceled、rename failure、temp cleanup。
- `WithOverwrite(false)` semantics documented；不能跨平台 race-free 时不得承诺 race-free。
- visibility chmod tested with umask-sensitive cases。
- directory visibility tested for `MakeDirectory` and auto-created parent dirs。
- MIME detection tested for content sniffing fallback。
- `ListPage` does not scan whole tree before paging；single huge directory cost documented。
- local list cursor is opaque and short-lived。
- `Exists` does not use prefix list inference。
- URL segment escaping tests cover spaces、`#`、`?`、Chinese chars、slash segment、repeated slash rejection。
- `BaseURL` trailing slash behavior fixed：`/storage/` + `a.txt` must not produce `/storage//a.txt`。
- README states `BaseURL` only generates URLs; applications must serve files themselves。

P0c Gate：

- `Delete` / `DeleteIfExists` / `DeleteMany` / `WithIgnoreMissing` behavior tested。
- `Copy` fallback tested。
- `Move` partial failure tested with `ErrPartialFailure`。
- `ReadOnly` blocks all write-class operations。
- `Scoped` strips prefix from list results and forwards capability registry accurately。
- `MakeDirectory` 不支持时返回 `ErrUnsupported`；只有 `DirectoryPrefixOnly` 可以 no-op。
- fake restores previous disk on cleanup；if original disk is missing, fake registration is removed。
- persistent fake keeps files。
- local driver passes all applicable layered contract tests。
- contract tests are reusable by future drivers。
- `go test -race ./...` passes for manager、fake replace、parallel disk ops。
- core package has no cloud vendor SDK dependency。
- README demonstrates default disk、named disk、public URL、temporary URL builder、fake disk、scoped disk、read-only disk、streaming write。
