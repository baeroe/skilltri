package actions

import "github.com/baeroe/skilltri/internal/config"

// ServerAdd adds the server name. The name and the server are validated; an existing name wraps
// ErrExists.
func ServerAdd(e Env, name string, s config.Server) error {
	return e.edit(func(c *config.Config) error {
		if c.Servers[name] != nil {
			return wrap(ErrExists, "server %q already exists", name)
		}
		if c.Servers == nil {
			c.Servers = map[string]*config.Server{}
		}
		c.Servers[name] = &s
		return nil
	})
}

// ServerUpdate replaces the server name with s but keeps Rules, Tools and Origin of the existing
// entry when s has them nil. A missing server wraps ErrNotFound.
func ServerUpdate(e Env, name string, s config.Server) error {
	return e.edit(func(c *config.Config) error {
		old := c.Servers[name]
		if old == nil {
			return wrap(ErrNotFound, "no such server %q", name)
		}
		if s.Rules == nil {
			s.Rules = old.Rules
		}
		if s.Tools == nil {
			s.Tools = old.Tools
		}
		if s.Origin == nil {
			s.Origin = old.Origin
		}
		c.Servers[name] = &s
		return nil
	})
}

// ServerRename renames old to new, keeping everything else. A missing old wraps ErrNotFound, an
// existing new wraps ErrExists; renaming to the same name changes nothing.
func ServerRename(e Env, old, new string) error {
	return e.edit(func(c *config.Config) error {
		s := c.Servers[old]
		if s == nil {
			return wrap(ErrNotFound, "no such server %q", old)
		}
		if old == new {
			return nil
		}
		if c.Servers[new] != nil {
			return wrap(ErrExists, "server %q already exists", new)
		}
		c.Servers[new] = s
		delete(c.Servers, old)
		return nil
	})
}

// ServerRemove removes the server name. A missing server wraps ErrNotFound.
func ServerRemove(e Env, name string) error {
	return e.edit(func(c *config.Config) error {
		if c.Servers[name] == nil {
			return wrap(ErrNotFound, "no such server %q", name)
		}
		delete(c.Servers, name)
		return nil
	})
}
