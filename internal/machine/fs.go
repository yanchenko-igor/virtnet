// Package machine implements the virtual machine layer (ARCHITECTURE.md §7.2,
// §7.7-7.8): machines, cooperative processes, the shell, the console, and the
// per-machine in-memory filesystem. Nothing here touches the host OS.
package machine

import (
	"fmt"
	"sort"
	"strings"
)

// FS is a minimal per-machine in-memory filesystem (ARCHITECTURE.md §7.8).
type FS struct {
	root *dirNode
}

type dirNode struct {
	name    string
	subdirs map[string]*dirNode
	files   map[string]*fileNode
}

type fileNode struct {
	data []byte
}

// NewFS mounts the standard directory skeleton.
func NewFS() *FS {
	fs := &FS{root: &dirNode{name: "/", subdirs: map[string]*dirNode{}, files: map[string]*fileNode{}}}
	for _, d := range []string{"bin", "etc", "home", "tmp", "var"} {
		fs.CreateDir("/" + d)
	}
	return fs
}

// resolve walks path from the root, creating nothing.
func (fs *FS) resolve(path string) (*dirNode, error) {
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("machine: path %q must be absolute", path)
	}
	cur := fs.root
	for _, part := range strings.Split(path, "/") {
		if part == "" {
			continue
		}
		next, ok := cur.subdirs[part]
		if !ok {
			return nil, fmt.Errorf("machine: no such directory: %s", path)
		}
		cur = next
	}
	return cur, nil
}

// CreateDir makes path (and any missing parents) as a directory.
func (fs *FS) CreateDir(path string) error {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	cur := fs.root
	for _, part := range parts {
		if part == "" {
			continue
		}
		next, ok := cur.subdirs[part]
		if !ok {
			next = &dirNode{name: part, subdirs: map[string]*dirNode{}, files: map[string]*fileNode{}}
			cur.subdirs[part] = next
		}
		cur = next
	}
	return nil
}

// WriteFile writes data to path, creating the file (and parents) as needed.
func (fs *FS) WriteFile(path string, data []byte) error {
	dir, name, err := fs.split(path)
	if err != nil {
		return err
	}
	dir.files[name] = &fileNode{data: append([]byte(nil), data...)}
	return nil
}

// ReadFile returns the contents of path.
func (fs *FS) ReadFile(path string) ([]byte, error) {
	dir, name, err := fs.split(path)
	if err != nil {
		return nil, err
	}
	f, ok := dir.files[name]
	if !ok {
		return nil, fmt.Errorf("machine: no such file: %s", path)
	}
	return append([]byte(nil), f.data...), nil
}

// ListDir returns the entries of path, sorted: subdirectories first, then
// files, each alphabetically (deterministic output for the shell).
func (fs *FS) ListDir(path string) ([]string, error) {
	dir, err := fs.resolve(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(dir.subdirs)+len(dir.files))
	for name := range dir.subdirs {
		names = append(names, name+"/")
	}
	for name := range dir.files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func (fs *FS) split(path string) (*dirNode, string, error) {
	idx := strings.LastIndex(path, "/")
	var dirPath, name string
	if idx < 0 {
		dirPath, name = "/", path
	} else {
		dirPath, name = path[:idx], path[idx+1:]
		if dirPath == "" {
			dirPath = "/"
		}
	}
	if name == "" {
		return nil, "", fmt.Errorf("machine: invalid path: %s", path)
	}
	dir, err := fs.resolve(dirPath)
	if err != nil {
		return nil, "", err
	}
	return dir, name, nil
}
