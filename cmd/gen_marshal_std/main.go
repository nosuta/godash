package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run gen_marshal_std.go <directory>")
		os.Exit(1)
	}

	dir := os.Args[1]
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		// Only parse standard .pb.go files, not lite or godash
		return strings.HasSuffix(fi.Name(), ".pb.go") && !strings.HasSuffix(fi.Name(), "_lite.go")
	}, 0)

	if err != nil {
		panic(err)
	}

	out, err := os.Create(filepath.Join(dir, "marshal_std_gen.go"))
	if err != nil {
		panic(err)
	}
	defer out.Close()

	fmt.Fprintln(out, "//go:build !js")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "package pb")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "import (")
	fmt.Fprintln(out, `	"google.golang.org/protobuf/proto"`)
	fmt.Fprintln(out, ")")

	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				ts, ok := n.(*ast.TypeSpec)
				if !ok {
					return true
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					return true
				}
				_ = st

				name := ts.Name.Name
				// Basic filter for Protobuf messages (exclude oneof wrappers like Request_Init)
				if !strings.Contains(name, "_") && ast.IsExported(name) {
					fmt.Fprintf(out, "\nfunc (m *%s) MarshalVT() ([]byte, error) {\n", name)
					fmt.Fprintf(out, "	return proto.Marshal(m)\n")
					fmt.Fprintf(out, "}\n")
					fmt.Fprintf(out, "\nfunc (m *%s) UnmarshalVT(b []byte) error {\n", name)
					fmt.Fprintf(out, "	return proto.Unmarshal(b, m)\n")
					fmt.Fprintf(out, "}\n")
				}
				return true
			})
		}
	}
}
