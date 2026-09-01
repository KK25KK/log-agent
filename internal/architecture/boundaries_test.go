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
		allowed string
	}{
		{prefix: "github.com/cloudwego/eino", allowed: filepath.Join("internal", "adapters", "eino")},
		{prefix: "github.com/larksuite/oapi-sdk-go", allowed: filepath.Join("internal", "adapters", "feishu")},
		{prefix: "os/exec", allowed: filepath.Join("internal", "adapters", "aliyuncli")},
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
				if strings.HasPrefix(value, rule.prefix) && !pathWithin(relative, rule.allowed) {
					t.Errorf("%s imports %s outside %s", relative, value, rule.allowed)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func pathWithin(path, directory string) bool {
	path = filepath.Clean(path)
	directory = filepath.Clean(directory)
	return path == directory || strings.HasPrefix(path, directory+string(filepath.Separator))
}
