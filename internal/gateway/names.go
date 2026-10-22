package gateway

import (
	"crypto/sha256"
	"encoding/hex"
)

// Version is the serverInfo version sessions report; main sets it from the build version.
var Version = "0.1.0"

// maxNameLen is the MCP limit for tool and prompt names.
const maxNameLen = 64

// ExposedName is the harness-facing name of a downstream tool or prompt: "<server>_<name>".
// Server names cannot contain "_", so prefixes never collide. Names over 64 characters keep
// their first 55 characters plus "_" and 8 hex digits of sha256(server+"/"+name).
func ExposedName(server, name string) string {
	full := server + "_" + name
	if len(full) <= maxNameLen {
		return full
	}
	sum := sha256.Sum256([]byte(server + "/" + name))
	return full[:55] + "_" + hex.EncodeToString(sum[:4])
}
