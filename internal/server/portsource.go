package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

// ErrPortReadOnly means the tunnel port is set where xs-server cannot change
// it (an environment variable, or no config file).
var ErrPortReadOnly = errors.New("tunnel port is set outside the server's config file")

// PortSource is where the tunnel port hubs dial is configured: the only
// place it lives. The web panel and xs-ctl read and change it there; nothing
// else keeps a copy. A change takes effect at the next server start.
type PortSource interface {
	// Name says where the setting lives: "ha" (the app's Network section),
	// "file" (server.yaml), "env" (XS_SERVER_PUBLIC_PORT) or "default".
	Name() string
	// Writable reports whether Set can change it.
	Writable() bool
	// Configured returns the configured port (0 = the main listen port).
	Configured(ctx context.Context) (int, error)
	// Set changes the configured port.
	Set(ctx context.Context, port int) error
}

// FixedPort is a read-only source: the port comes from the environment or
// from defaults (no config file).
type FixedPort struct {
	From string // "env" or "default"
	Port int
}

// Name implements PortSource.
func (f FixedPort) Name() string { return f.From }

// Writable implements PortSource.
func (FixedPort) Writable() bool { return false }

// Configured implements PortSource.
func (f FixedPort) Configured(context.Context) (int, error) { return f.Port, nil }

// Set implements PortSource.
func (FixedPort) Set(context.Context, int) error { return ErrPortReadOnly }

// FilePort keeps the port as public_port in server.yaml. Set rewrites only
// that key and keeps the rest of the file, comments included.
type FilePort struct{ Path string }

// Name implements PortSource.
func (FilePort) Name() string { return "file" }

// Writable implements PortSource.
func (FilePort) Writable() bool { return true }

// Configured implements PortSource.
func (f FilePort) Configured(context.Context) (int, error) {
	doc, err := f.read()
	if err != nil {
		return 0, err
	}
	if v := mappingValue(doc, "public_port"); v != nil {
		n, err := strconv.Atoi(v.Value)
		if err != nil {
			return 0, fmt.Errorf("%s: public_port: not an integer", f.Path)
		}
		return n, nil
	}
	return 0, nil
}

// Set implements PortSource.
func (f FilePort) Set(_ context.Context, port int) error {
	doc, err := f.read()
	if err != nil {
		return err
	}
	if doc.Kind == 0 {
		doc = &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("%s: not a YAML mapping", f.Path)
	}
	val := strconv.Itoa(port)
	if v := mappingValue(doc, "public_port"); v != nil {
		v.Kind, v.Tag, v.Value, v.Style = yaml.ScalarNode, "!!int", val, 0
	} else {
		m := doc.Content[0]
		m.Content = append(m.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "public_port"},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: val})
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("%s: encode: %w", f.Path, err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("%s: encode: %w", f.Path, err)
	}
	return writeFileAtomic(f.Path, buf.Bytes())
}

// read parses the file; a missing or empty file is an empty node.
func (f FilePort) read() (*yaml.Node, error) {
	b, err := os.ReadFile(f.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return &yaml.Node{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", f.Path, err)
	}
	return &doc, nil
}

// mappingValue returns the value node of key in a document's top mapping.
func mappingValue(doc *yaml.Node, key string) *yaml.Node {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil
	}
	m := doc.Content[0].Content
	for i := 0; i+1 < len(m); i += 2 {
		if m[i].Value == key {
			return m[i+1]
		}
	}
	return nil
}
