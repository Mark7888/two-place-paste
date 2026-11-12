// Command specjson renders the admin API description as JSON, so the relay can
// serve it in both formats without a YAML parser of its own.
//
//	specjson openapi.yaml openapi.json
//
// Keys keep their order from the YAML file, and the output is indented and
// ends in a newline, so it is the same bytes every time for the same input:
// `make api-check` compares it with the committed copy.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"go.yaml.in/yaml/v3"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: specjson IN.yaml OUT.json")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, "specjson:", err)
		os.Exit(1)
	}
}

func run(in, out string) error {
	src, err := os.ReadFile(in)
	if err != nil {
		return fmt.Errorf("read %s: %w", in, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", in, err)
	}
	var buf bytes.Buffer
	if err := write(&buf, &doc); err != nil {
		return fmt.Errorf("convert %s: %w", in, err)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, buf.Bytes(), "", "  "); err != nil {
		return fmt.Errorf("indent: %w", err)
	}
	pretty.WriteByte('\n')
	if err := os.WriteFile(out, pretty.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", out, err)
	}
	return nil
}

// write emits a YAML node as compact JSON. It walks the node tree rather than
// decoding into maps, because Go maps would lose the key order.
func write(buf *bytes.Buffer, n *yaml.Node) error {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) != 1 {
			return errors.New("expected one document")
		}
		return write(buf, n.Content[0])
	case yaml.AliasNode:
		return write(buf, n.Alias)
	case yaml.MappingNode:
		buf.WriteByte('{')
		for i := 0; i+1 < len(n.Content); i += 2 {
			if i > 0 {
				buf.WriteByte(',')
			}
			k, err := json.Marshal(n.Content[i].Value)
			if err != nil {
				return err
			}
			buf.Write(k)
			buf.WriteByte(':')
			if err := write(buf, n.Content[i+1]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	case yaml.SequenceNode:
		buf.WriteByte('[')
		for i, c := range n.Content {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := write(buf, c); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case yaml.ScalarNode:
		var v any
		if err := n.Decode(&v); err != nil {
			return err
		}
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("line %d: %w", n.Line, err)
		}
		buf.Write(b)
	default:
		return fmt.Errorf("line %d: unsupported YAML node", n.Line)
	}
	return nil
}
