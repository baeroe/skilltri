package marketplace

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Remote is a plugin that lives in a git repository.
type Remote struct {
	URL  string // git URL; "owner/repo" shorthand is kept as written
	Ref  string // sha if given, else ref
	Path string // subdirectory, "" = repository root
}

// ParseSource interprets a plugin source. A relative string ("./x") is
// returned as local (unchanged); git based sources as remote. npm, archive
// and command sources are not supported.
func ParseSource(raw json.RawMessage) (local string, remote *Remote, err error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil, errors.New("plugin source missing")
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if !strings.HasPrefix(s, "./") {
			return "", nil, fmt.Errorf("plugin source %q must be a relative path starting with ./", s)
		}
		if _, err := safeRel(s); err != nil {
			return "", nil, fmt.Errorf("plugin source %w", err)
		}
		return s, nil, nil
	}
	var o struct {
		Source string `json:"source"`
		Repo   string `json:"repo"`
		URL    string `json:"url"`
		Path   string `json:"path"`
		Ref    string `json:"ref"`
		SHA    string `json:"sha"`
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		return "", nil, fmt.Errorf("parse plugin source: %w", err)
	}
	r := &Remote{Ref: o.Ref}
	if o.SHA != "" {
		r.Ref = o.SHA
	}
	switch o.Source {
	case "github":
		if o.Repo == "" {
			return "", nil, errors.New("github plugin source needs repo")
		}
		r.URL = o.Repo
	case "url":
		if o.URL == "" {
			return "", nil, errors.New("url plugin source needs url")
		}
		r.URL = o.URL
	case "git-subdir":
		r.URL = o.URL
		if r.URL == "" {
			r.URL = o.Repo
		}
		if r.URL == "" {
			return "", nil, errors.New("git-subdir plugin source needs url or repo")
		}
		if o.Path != "" {
			p, err := safeRel(o.Path)
			if err != nil {
				return "", nil, fmt.Errorf("git-subdir path %w", err)
			}
			if p != "." {
				r.Path = p
			}
		}
	default:
		return "", nil, fmt.Errorf("unsupported plugin source %s", o.Source)
	}
	return "", r, nil
}
