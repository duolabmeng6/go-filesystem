package gitdriver

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/duolabmeng6/go-filesystem"
)

var scpLikeURLPattern = regexp.MustCompile(`^([^@]+)@([^:]+):(.+)$`)

type URLInfo struct {
	Raw            string `json:"raw"`
	Protocol       string `json:"protocol"`
	Platform       string `json:"platform"`
	User           string `json:"user,omitempty"`
	RepositoryPath string `json:"repository_path"`
	Owner          string `json:"owner,omitempty"`
	Repo           string `json:"repo"`
}

func ParseURL(raw string) (URLInfo, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return URLInfo{}, fmt.Errorf("%w: git url is required", filesystem.ErrInvalidPath)
	}
	if match := scpLikeURLPattern.FindStringSubmatch(raw); match != nil {
		return urlInfoFromParts(raw, "ssh", match[2], match[1], match[3])
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return URLInfo{}, fmt.Errorf("%w: git url is invalid", filesystem.ErrInvalidPath)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return urlInfoFromParts(raw, "https", parsed.Hostname(), parsed.User.Username(), parsed.Path)
	case "ssh":
		return urlInfoFromParts(raw, "ssh", parsed.Hostname(), parsed.User.Username(), parsed.Path)
	case "file":
		return urlInfoFromFile(raw, parsed.Path)
	default:
		return URLInfo{}, fmt.Errorf("%w: git url protocol is unsupported", filesystem.ErrInvalidPath)
	}
}

func urlInfoFromParts(raw string, protocol string, host string, user string, repoPath string) (URLInfo, error) {
	host = strings.TrimSpace(host)
	repoPath = strings.Trim(strings.TrimSpace(repoPath), "/")
	if host == "" || repoPath == "" {
		return URLInfo{}, fmt.Errorf("%w: git url is incomplete", filesystem.ErrInvalidPath)
	}
	repoPath = strings.TrimSuffix(repoPath, ".git")
	repoPath = path.Clean(repoPath)
	if repoPath == "." || strings.HasPrefix(repoPath, "../") {
		return URLInfo{}, fmt.Errorf("%w: git repository path is invalid", filesystem.ErrInvalidPath)
	}
	repo := path.Base(repoPath)
	owner := path.Dir(repoPath)
	if owner == "." {
		owner = ""
	}
	platform := strings.TrimPrefix(strings.ToLower(host), "www.")
	return URLInfo{
		Raw:            raw,
		Protocol:       protocol,
		Platform:       platform,
		User:           strings.TrimSpace(user),
		RepositoryPath: repoPath,
		Owner:          owner,
		Repo:           repo,
	}, nil
}

func urlInfoFromFile(raw string, repoPath string) (URLInfo, error) {
	repoPath = path.Clean(strings.TrimSpace(repoPath))
	if repoPath == "" || repoPath == "." {
		return URLInfo{}, fmt.Errorf("%w: git file url is incomplete", filesystem.ErrInvalidPath)
	}
	repo := path.Base(strings.TrimSuffix(repoPath, ".git"))
	return URLInfo{
		Raw:            raw,
		Protocol:       "file",
		Platform:       "local",
		RepositoryPath: repoPath,
		Repo:           repo,
	}, nil
}

func RepositoryNameFromURL(raw string) string {
	info, err := ParseURL(raw)
	if err != nil {
		return ""
	}
	return info.Repo
}
