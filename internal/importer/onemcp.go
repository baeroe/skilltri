// Package importer converts other tools' MCP configs into skilltri servers. It is pure:
// secrets are returned separately so the caller decides where they are stored.
package importer

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/secrets"
)

// Result is the outcome of an import.
type Result struct {
	Servers  map[string]*config.Server
	Secrets  map[string]string // secret name → value, referenced from Servers
	Warnings []string
}

type oneMCPServer struct {
	Type           string            `json:"type"`
	Command        string            `json:"command"`
	Args           []string          `json:"args"`
	Env            map[string]string `json:"env"`
	URL            string            `json:"url"`
	Headers        map[string]string `json:"headers"`
	Tags           []string          `json:"tags"`
	RequestTimeout int               `json:"requestTimeout"`
	Disabled       bool              `json:"disabled"`
}

var (
	invalidNameChars = regexp.MustCompile(`[^A-Za-z0-9-]`)
	invalidSecretCh  = regexp.MustCompile(`[^A-Za-z0-9._-]`)
	secretKey        = regexp.MustCompile(`(?i)(token|key|secret|password|auth)`)
)

// From1MCP converts the content of a 1MCP mcp.json. Header values and sensitive-looking env
// values are replaced by ${secret:NAME} references; the values land in Result.Secrets.
func From1MCP(data []byte) (Result, error) {
	var file struct {
		MCPServers map[string]oneMCPServer `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return Result{}, fmt.Errorf("parse 1MCP config: %w", err)
	}
	res := Result{Servers: map[string]*config.Server{}, Secrets: map[string]string{}}
	names := make([]string, 0, len(file.MCPServers))
	for n := range file.MCPServers {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, old := range names {
		in := file.MCPServers[old]
		name := old
		if !config.ValidServerName(name) {
			name = invalidNameChars.ReplaceAllString(name, "-")
			if !config.ValidServerName(name) {
				res.warn("skipped %q: not a valid server name", old)
				continue
			}
			res.warn("renamed %q to %q: not a valid server name", old, name)
		}
		if _, dup := res.Servers[name]; dup {
			res.warn("skipped %q: name %q is already taken", old, name)
			continue
		}
		srv := &config.Server{Disabled: in.Disabled, RequestTimeoutMs: in.RequestTimeout}
		switch t := strings.ToLower(in.Type); {
		case t == "sse":
			res.warn("skipped %q: type sse is not supported", old)
			continue
		case t == "http" || t == "streamablehttp" || (t == "" && in.URL != "" && in.Command == ""):
			srv.Type, srv.URL = "http", in.URL
		case t == "stdio" || (t == "" && in.Command != ""):
			srv.Type, srv.Command, srv.Args = "stdio", in.Command, in.Args
		default:
			res.warn("skipped %q: cannot determine server type", old)
			continue
		}
		if len(in.Headers) > 0 {
			srv.Headers = map[string]string{}
			for k, v := range in.Headers {
				srv.Headers[k] = res.secret(name, k, v)
			}
		}
		if len(in.Env) > 0 {
			srv.Env = map[string]string{}
			for k, v := range in.Env {
				if secretKey.MatchString(k) {
					v = res.secret(name, k, v)
				}
				srv.Env[k] = v
			}
		}
		if len(in.Tags) > 0 {
			res.warn("%q: tags (%s) are not imported, set folder rules instead", name, strings.Join(in.Tags, ", "))
		}
		res.Servers[name] = srv
	}
	return res, nil
}

func (r *Result) warn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// secret stores value as <server>-<lowercased key> and returns the reference.
func (r *Result) secret(server, key, value string) string {
	if value == "" {
		return value
	}
	name := server + "-" + invalidSecretCh.ReplaceAllString(strings.ToLower(key), "-")
	r.Secrets[name] = value
	return secrets.Ref(name)
}
