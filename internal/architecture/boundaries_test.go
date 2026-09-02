package architecture

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestFrameworkImportsStayInsideAdapters(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	rules := []struct {
		prefix  string
		allowed []string
	}{
		{prefix: "github.com/cloudwego/eino", allowed: []string{filepath.Join("internal", "adapters", "eino")}},
		{prefix: "github.com/larksuite/oapi-sdk-go", allowed: []string{filepath.Join("internal", "adapters", "feishu")}},
		{prefix: "os/exec", allowed: []string{filepath.Join("internal", "adapters", "aliyuncli"), filepath.Join("internal", "adapters", "gitcode")}},
	}

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imported := range file.Imports {
			value, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return err
			}
			for _, rule := range rules {
				if strings.HasPrefix(value, rule.prefix) && !pathWithinAny(relative, rule.allowed) {
					t.Errorf("%s imports %s outside %v", relative, value, rule.allowed)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func pathWithinAny(path string, directories []string) bool {
	for _, directory := range directories {
		if pathWithin(path, directory) {
			return true
		}
	}
	return false
}

func pathWithin(path, directory string) bool {
	path = filepath.Clean(path)
	directory = filepath.Clean(directory)
	return path == directory || strings.HasPrefix(path, directory+string(filepath.Separator))
}
