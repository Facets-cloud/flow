package claude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Settings I/O for ~/.claude/settings.json and the plugin files.
//
// Writes are atomic (a temp file in the same directory, synced, then
// renamed over the target), so a concurrent Claude startup never reads a
// truncated file and a failed write leaves the old file intact. A symlinked
// settings.json (dotfile managers) is written through to its target, and
// the existing file mode is kept.
//
// Edits of the user's settings.json go through an order-preserving decode
// (objects become *ordObj), so removing flow's entries leaves every other
// key where the user put it, and numbers keep their literal form.

// writeFileAtomic replaces path with content. perm applies only when path
// does not exist yet.
func writeFileAtomic(path string, content []byte, perm os.FileMode) error {
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}
	if fi, err := os.Stat(path); err == nil {
		perm = fi.Mode().Perm()
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".flow-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after a successful rename
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// ordObj is a JSON object that remembers its key order.
type ordObj struct {
	keys []string
	vals map[string]any
}

func newOrdObj() *ordObj { return &ordObj{vals: map[string]any{}} }

func (o *ordObj) get(k string) (any, bool) {
	v, ok := o.vals[k]
	return v, ok
}

// set updates k in place, or appends it.
func (o *ordObj) set(k string, v any) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

func (o *ordObj) del(k string) {
	if _, ok := o.vals[k]; !ok {
		return
	}
	delete(o.vals, k)
	for i, kk := range o.keys {
		if kk == k {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

func (o *ordObj) len() int { return len(o.keys) }

// decodeOrdered parses raw into ordered values: *ordObj, []any, string,
// json.Number, bool or nil. The top level must be one JSON value.
func decodeOrdered(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, err := decodeOrdValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing data after JSON value")
	}
	return v, nil
}

func decodeOrdValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := newOrdObj()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, _ := kt.(string)
				v, err := decodeOrdValue(dec)
				if err != nil {
					return nil, err
				}
				o.set(k, v) // a duplicate key keeps its first position, last value (like encoding/json)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return o, nil
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := decodeOrdValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return arr, nil
		}
		return nil, fmt.Errorf("unexpected %v", t)
	default:
		return tok, nil
	}
}

// encodeOrdered renders v with a two-space indent and a trailing newline,
// the layout json.MarshalIndent produces, without HTML escaping.
func encodeOrdered(v any) ([]byte, error) {
	var b bytes.Buffer
	if err := writeOrd(&b, v, 0); err != nil {
		return nil, err
	}
	b.WriteByte('\n')
	return b.Bytes(), nil
}

func writeOrd(b *bytes.Buffer, v any, depth int) error {
	ind := func(d int) string { return strings.Repeat("  ", d) }
	switch t := v.(type) {
	case *ordObj:
		if t.len() == 0 {
			b.WriteString("{}")
			return nil
		}
		b.WriteString("{\n")
		for i, k := range t.keys {
			b.WriteString(ind(depth + 1))
			if err := writeScalar(b, k); err != nil {
				return err
			}
			b.WriteString(": ")
			if err := writeOrd(b, t.vals[k], depth+1); err != nil {
				return err
			}
			if i < len(t.keys)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(ind(depth) + "}")
	case []any:
		if len(t) == 0 {
			b.WriteString("[]")
			return nil
		}
		b.WriteString("[\n")
		for i, e := range t {
			b.WriteString(ind(depth + 1))
			if err := writeOrd(b, e, depth+1); err != nil {
				return err
			}
			if i < len(t)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(ind(depth) + "]")
	default:
		return writeScalar(b, t)
	}
	return nil
}

func writeScalar(b *bytes.Buffer, v any) error {
	if n, ok := v.(json.Number); ok {
		b.WriteString(n.String())
		return nil
	}
	var sb bytes.Buffer
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	b.Write(bytes.TrimRight(sb.Bytes(), "\n"))
	return nil
}
