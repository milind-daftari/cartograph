package query

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cloudprivacylabs/lpg/v2"

	"github.com/realxen/cartograph/internal/graph"
	"github.com/realxen/cartograph/internal/service"
)

func addPackageMapFile(g *lpg.Graph, filePath string) *lpg.Node {
	return graph.AddFileNode(g, graph.FileProps{
		BaseNodeProps: graph.BaseNodeProps{ID: "file:" + filePath, Name: filePath},
		FilePath:      filePath,
		Language:      "go",
	})
}

func edgeByPackage(imports []service.PackageMapImport, from, to string) (service.PackageMapImport, bool) {
	for _, imp := range imports {
		if imp.From == from && imp.To == to {
			return imp, true
		}
	}
	return service.PackageMapImport{}, false
}

func buildPackageMapFixture() *lpg.Graph {
	g := lpg.NewGraph()
	root := addPackageMapFile(g, "main.go")
	apiHandler := addPackageMapFile(g, "internal/api/handler.go")
	apiRoutes := addPackageMapFile(g, "internal/api/routes.go")
	authService := addPackageMapFile(g, "internal/auth/service.go")
	authTest := addPackageMapFile(g, "internal/auth/token_test.go")
	dbStore := addPackageMapFile(g, "internal/db/store.go")

	graph.AddEdge(g, root, apiHandler, graph.RelImports, nil)
	graph.AddEdge(g, apiHandler, authService, graph.RelImports, nil)
	graph.AddEdge(g, apiHandler, authService, graph.RelImports, nil) // duplicate file pair should be deduped
	graph.AddEdge(g, apiRoutes, authService, graph.RelImports, nil)
	graph.AddEdge(g, authService, dbStore, graph.RelImports, nil)
	graph.AddEdge(g, apiHandler, apiRoutes, graph.RelImports, nil) // same package should be skipped
	graph.AddEdge(g, authTest, dbStore, graph.RelImports, nil)     // skipped unless IncludeTests

	fromFolder := graph.AddFolderNode(g, graph.FolderProps{
		BaseNodeProps: graph.BaseNodeProps{ID: "folder:internal/api", Name: "api"},
		FilePath:      "internal/api",
	})
	toFolder := graph.AddFolderNode(g, graph.FolderProps{
		BaseNodeProps: graph.BaseNodeProps{ID: "folder:internal/auth", Name: "auth"},
		FilePath:      "internal/auth",
	})
	graph.AddEdge(g, fromFolder, toFolder, graph.RelImports, nil)

	fn := graph.AddSymbolNode(g, graph.LabelFunction, graph.SymbolProps{
		BaseNodeProps: graph.BaseNodeProps{ID: "func:handler", Name: "handler"},
		FilePath:      "internal/api/handler.go",
	})
	graph.AddEdge(g, fn, dbStore, graph.RelImports, nil)
	return g
}

func TestPackageMap_AggregatesResolvedFileImports(t *testing.T) {
	b := &Backend{Graph: buildPackageMapFixture()}
	result, err := b.PackageMap(service.PackageMapRequest{Repo: "test", Format: "json", Limit: 100})
	if err != nil {
		t.Fatalf("PackageMap: %v", err)
	}
	if result.Repo != "test" {
		t.Fatalf("Repo = %q, expected test", result.Repo)
	}
	if result.Summary.TotalEdges != 3 {
		t.Fatalf("TotalEdges = %d, expected 3", result.Summary.TotalEdges)
	}
	if result.Summary.TotalImports != 4 {
		t.Fatalf("TotalImports = %d, expected 4", result.Summary.TotalImports)
	}
	if imp, ok := edgeByPackage(result.Imports, "internal/api", "internal/auth"); !ok {
		t.Fatal("expected internal/api -> internal/auth")
	} else if imp.Count != 2 || imp.SourceFileCount != 2 {
		t.Fatalf("api->auth = count %d source files %d, expected 2/2", imp.Count, imp.SourceFileCount)
	}
	if imp, ok := edgeByPackage(result.Imports, ".", "internal/api"); !ok {
		t.Fatal("expected . -> internal/api")
	} else if imp.Count != 1 {
		t.Fatalf(".->api count = %d, expected 1", imp.Count)
	}
}

func TestPackageMap_IgnoresFolderImportEdges(t *testing.T) {
	b := &Backend{Graph: buildPackageMapFixture()}
	result, err := b.PackageMap(service.PackageMapRequest{Repo: "test", Format: "json", Limit: 100})
	if err != nil {
		t.Fatalf("PackageMap: %v", err)
	}
	if imp, ok := edgeByPackage(result.Imports, "internal/api", "internal/auth"); !ok {
		t.Fatal("expected internal/api -> internal/auth")
	} else if imp.Count != 2 {
		t.Fatalf("api->auth count = %d, expected 2 without derived folder edge", imp.Count)
	}
}

func TestPackageMap_IgnoresNonFileImportEdges(t *testing.T) {
	b := &Backend{Graph: buildPackageMapFixture()}
	result, err := b.PackageMap(service.PackageMapRequest{Repo: "test", Format: "json", Limit: 100})
	if err != nil {
		t.Fatalf("PackageMap: %v", err)
	}
	if _, ok := edgeByPackage(result.Imports, "internal/api", "internal/db"); ok {
		t.Fatal("unexpected Function -> File IMPORTS edge in package map")
	}
}

func TestPackageMap_HandlesRootPackage(t *testing.T) {
	b := &Backend{Graph: buildPackageMapFixture()}
	result, err := b.PackageMap(service.PackageMapRequest{Repo: "test", Format: "json", Limit: 100})
	if err != nil {
		t.Fatalf("PackageMap: %v", err)
	}
	if imp, ok := edgeByPackage(result.Imports, ".", "internal/api"); !ok {
		t.Fatal("expected root package import . -> internal/api")
	} else if imp.Count != 1 {
		t.Fatalf(".->internal/api count = %d, expected 1", imp.Count)
	}
}

func TestPackageMap_DeduplicatesFilePairs(t *testing.T) {
	b := &Backend{Graph: buildPackageMapFixture()}
	result, err := b.PackageMap(service.PackageMapRequest{Repo: "test", Format: "json", Limit: 100, IncludeFiles: true})
	if err != nil {
		t.Fatalf("PackageMap: %v", err)
	}
	imp, ok := edgeByPackage(result.Imports, "internal/api", "internal/auth")
	if !ok {
		t.Fatal("expected internal/api -> internal/auth")
	}
	if imp.Count != 2 {
		t.Fatalf("api->auth count = %d, expected 2 after duplicate file-pair dedupe", imp.Count)
	}
	for _, pair := range imp.Files {
		if pair.FromFile == "internal/api/handler.go" && pair.ToFile == "internal/auth/service.go" {
			return
		}
	}
	t.Fatalf("expected deduped handler.go -> service.go file evidence, got %#v", imp.Files)
}

func TestPackageMap_SkipsSamePackageImports(t *testing.T) {
	b := &Backend{Graph: buildPackageMapFixture()}
	result, err := b.PackageMap(service.PackageMapRequest{Repo: "test", Format: "json", Limit: 100})
	if err != nil {
		t.Fatalf("PackageMap: %v", err)
	}
	if _, ok := edgeByPackage(result.Imports, "internal/api", "internal/api"); ok {
		t.Fatal("same-package import should be skipped")
	}
	if result.Summary.SkippedSelfImports != 1 {
		t.Fatalf("SkippedSelfImports = %d, expected 1", result.Summary.SkippedSelfImports)
	}
}

func TestPackageMap_ExcludeTestsByDefault(t *testing.T) {
	b := &Backend{Graph: buildPackageMapFixture()}
	result, err := b.PackageMap(service.PackageMapRequest{Repo: "test", Format: "json", Limit: 100})
	if err != nil {
		t.Fatalf("PackageMap: %v", err)
	}
	if imp, ok := edgeByPackage(result.Imports, "internal/auth", "internal/db"); !ok {
		t.Fatal("expected non-test auth -> db import")
	} else if imp.Count != 1 {
		t.Fatalf("auth->db count = %d, expected 1 without test import", imp.Count)
	}
	if result.Summary.SkippedTestImports != 1 {
		t.Fatalf("SkippedTestImports = %d, expected 1", result.Summary.SkippedTestImports)
	}
}

func TestPackageMap_IncludeTests(t *testing.T) {
	b := &Backend{Graph: buildPackageMapFixture()}
	result, err := b.PackageMap(service.PackageMapRequest{Repo: "test", Format: "json", Limit: 100, IncludeTests: true})
	if err != nil {
		t.Fatalf("PackageMap: %v", err)
	}
	if imp, ok := edgeByPackage(result.Imports, "internal/auth", "internal/db"); !ok {
		t.Fatal("expected auth -> db import")
	} else if imp.Count != 2 {
		t.Fatalf("auth->db count = %d, expected 2 with test import", imp.Count)
	}
}

func TestPackageMap_MinCount(t *testing.T) {
	b := &Backend{Graph: buildPackageMapFixture()}
	result, err := b.PackageMap(service.PackageMapRequest{Repo: "test", Format: "json", Limit: 100, MinCount: 2})
	if err != nil {
		t.Fatalf("PackageMap: %v", err)
	}
	if len(result.Imports) != 1 {
		t.Fatalf("len(Imports) = %d, expected 1", len(result.Imports))
	}
	if result.Imports[0].From != "internal/api" || result.Imports[0].To != "internal/auth" {
		t.Fatalf("only import = %s -> %s, expected internal/api -> internal/auth", result.Imports[0].From, result.Imports[0].To)
	}
}

func TestPackageMap_LimitAndTruncation(t *testing.T) {
	b := &Backend{Graph: buildPackageMapFixture()}
	result, err := b.PackageMap(service.PackageMapRequest{Repo: "test", Format: "json", Limit: 2})
	if err != nil {
		t.Fatalf("PackageMap: %v", err)
	}
	if len(result.Imports) != 2 {
		t.Fatalf("len(Imports) = %d, expected 2", len(result.Imports))
	}
	if !result.Summary.Truncated {
		t.Fatal("expected truncated summary")
	}
	if result.Summary.TotalEdges != 3 || result.Summary.ShownEdges != 2 {
		t.Fatalf("summary edges = %d/%d, expected 3/2", result.Summary.TotalEdges, result.Summary.ShownEdges)
	}
}

func TestPackageMap_DefaultLimits(t *testing.T) {
	g := lpg.NewGraph()
	root := addPackageMapFile(g, "main.go")
	for i := 0; i < 550; i++ {
		to := addPackageMapFile(g, fmt.Sprintf("pkg%03d/file.go", i))
		graph.AddEdge(g, root, to, graph.RelImports, nil)
	}
	b := &Backend{Graph: g}

	jsonResult, err := b.PackageMap(service.PackageMapRequest{Repo: "test", Format: "json"})
	if err != nil {
		t.Fatalf("PackageMap JSON: %v", err)
	}
	if len(jsonResult.Imports) != 500 || jsonResult.Summary.ShownEdges != 500 || !jsonResult.Summary.Truncated {
		t.Fatalf("JSON default limit summary = imports %d shown %d truncated %v, expected 500/500/true",
			len(jsonResult.Imports), jsonResult.Summary.ShownEdges, jsonResult.Summary.Truncated)
	}

	mermaidResult, err := b.PackageMap(service.PackageMapRequest{Repo: "test", Format: "mermaid"})
	if err != nil {
		t.Fatalf("PackageMap Mermaid: %v", err)
	}
	if len(mermaidResult.Imports) != 100 || mermaidResult.Summary.ShownEdges != 100 || !mermaidResult.Summary.Truncated {
		t.Fatalf("Mermaid default limit summary = imports %d shown %d truncated %v, expected 100/100/true",
			len(mermaidResult.Imports), mermaidResult.Summary.ShownEdges, mermaidResult.Summary.Truncated)
	}

	dotResult, err := b.PackageMap(service.PackageMapRequest{Repo: "test", Format: "dot"})
	if err != nil {
		t.Fatalf("PackageMap DOT: %v", err)
	}
	if len(dotResult.Imports) != 100 || dotResult.Summary.ShownEdges != 100 || !dotResult.Summary.Truncated {
		t.Fatalf("DOT default limit summary = imports %d shown %d truncated %v, expected 100/100/true",
			len(dotResult.Imports), dotResult.Summary.ShownEdges, dotResult.Summary.Truncated)
	}
}

func TestPackageMap_DeterministicOrdering(t *testing.T) {
	b := &Backend{Graph: buildPackageMapFixture()}
	result, err := b.PackageMap(service.PackageMapRequest{Repo: "test", Format: "json", Limit: 100})
	if err != nil {
		t.Fatalf("PackageMap: %v", err)
	}
	got := make([]string, 0, len(result.Imports))
	for _, imp := range result.Imports {
		got = append(got, fmt.Sprintf("%s->%s:%d", imp.From, imp.To, imp.Count))
	}
	want := []string{
		"internal/api->internal/auth:2",
		".->internal/api:1",
		"internal/auth->internal/db:1",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("order mismatch:\ngot:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestPackageMap_IncludeFilesBoundsEvidence(t *testing.T) {
	g := lpg.NewGraph()
	target := addPackageMapFile(g, "internal/auth/service.go")
	for i := 0; i < 7; i++ {
		from := addPackageMapFile(g, fmt.Sprintf("internal/api/file%d.go", i))
		graph.AddEdge(g, from, target, graph.RelImports, nil)
	}

	b := &Backend{Graph: g}
	result, err := b.PackageMap(service.PackageMapRequest{Repo: "test", Format: "json", IncludeFiles: true, Limit: 100})
	if err != nil {
		t.Fatalf("PackageMap: %v", err)
	}
	imp, ok := edgeByPackage(result.Imports, "internal/api", "internal/auth")
	if !ok {
		t.Fatal("expected internal/api -> internal/auth")
	}
	if len(imp.Files) != 5 {
		t.Fatalf("len(Files) = %d, expected bounded 5", len(imp.Files))
	}
	if !imp.FilesTruncated {
		t.Fatal("expected FilesTruncated")
	}
}

func TestPackageMap_MermaidAndDOTContent(t *testing.T) {
	b := &Backend{Graph: buildPackageMapFixture()}
	mermaid, err := b.PackageMap(service.PackageMapRequest{Repo: "test", Format: "mermaid", Limit: 100})
	if err != nil {
		t.Fatalf("PackageMap mermaid: %v", err)
	}
	if !strings.HasPrefix(mermaid.Content, "flowchart LR\n") {
		t.Fatalf("mermaid content should start with flowchart LR, got %q", mermaid.Content)
	}
	if strings.Contains(mermaid.Content, "internal/api -->") {
		t.Fatal("mermaid should use generated node IDs, not raw package paths")
	}

	dot, err := b.PackageMap(service.PackageMapRequest{Repo: "test", Format: "dot", Limit: 100})
	if err != nil {
		t.Fatalf("PackageMap dot: %v", err)
	}
	if !strings.HasPrefix(dot.Content, "digraph PackageMap {\n") {
		t.Fatalf("dot content should start with digraph, got %q", dot.Content)
	}
	if strings.Contains(dot.Content, "internal/api ->") {
		t.Fatal("dot should use generated node IDs, not raw package paths")
	}
}

func TestPackageMap_InvalidFormat(t *testing.T) {
	b := &Backend{Graph: buildPackageMapFixture()}
	if _, err := b.PackageMap(service.PackageMapRequest{Repo: "test", Format: "svg"}); err == nil {
		t.Fatal("expected invalid format error")
	}
}

func TestPackageMap_LargeGraphPerformanceShape(t *testing.T) {
	g := lpg.NewGraph()
	files := make([]*lpg.Node, 0, 12000)
	for pkg := 0; pkg < 300; pkg++ {
		for file := 0; file < 40; file++ {
			files = append(files, addPackageMapFile(g, fmt.Sprintf("pkg%03d/file%02d.go", pkg, file)))
		}
	}
	for i := 0; i < len(files); i++ {
		graph.AddEdge(g, files[i], files[(i+40)%len(files)], graph.RelImports, nil)
	}

	b := &Backend{Graph: g}
	result, err := b.PackageMap(service.PackageMapRequest{Repo: "large", Format: "json", Limit: 50})
	if err != nil {
		t.Fatalf("PackageMap: %v", err)
	}
	if result.Summary.TotalEdges != 300 {
		t.Fatalf("TotalEdges = %d, expected 300 package edges", result.Summary.TotalEdges)
	}
	if result.Summary.TotalImports != 12000 {
		t.Fatalf("TotalImports = %d, expected 12000 file imports", result.Summary.TotalImports)
	}
	if len(result.Imports) != 50 || !result.Summary.Truncated {
		t.Fatalf("shown/truncated = %d/%v, expected 50/true", len(result.Imports), result.Summary.Truncated)
	}
}

func TestPackageMap_EscapesUnsafeLabels(t *testing.T) {
	g := lpg.NewGraph()
	unsafeFrom := addPackageMapFile(g, "cmd|api/pkg/quote\"/from.go")
	unsafeTo := addPackageMapFile(g, "pkg/a] --> injected/pkg/<script>&/pkg/new\nline/pkg/back\\slash/to.go")
	graph.AddEdge(g, unsafeFrom, unsafeTo, graph.RelImports, nil)

	b := &Backend{Graph: g}
	mermaid, err := b.PackageMap(service.PackageMapRequest{Repo: "test", Format: "mermaid", Limit: 100})
	if err != nil {
		t.Fatalf("PackageMap mermaid: %v", err)
	}
	if strings.Contains(mermaid.Content, "|api") || strings.Contains(mermaid.Content, "] --> injected") || strings.Contains(mermaid.Content, "<script>") {
		t.Fatalf("mermaid content contains unescaped unsafe label text:\n%s", mermaid.Content)
	}
	if !strings.Contains(mermaid.Content, "&#124;") || !strings.Contains(mermaid.Content, "&lt;script&gt;") {
		t.Fatalf("mermaid content missing escaped label text:\n%s", mermaid.Content)
	}

	dot, err := b.PackageMap(service.PackageMapRequest{Repo: "test", Format: "dot", Limit: 100})
	if err != nil {
		t.Fatalf("PackageMap dot: %v", err)
	}
	if strings.Contains(dot.Content, "quote\"") || strings.Contains(dot.Content, "new\nline") {
		t.Fatalf("DOT content contains unescaped unsafe label text:\n%s", dot.Content)
	}
	if !strings.Contains(dot.Content, "quote\\\"") || !strings.Contains(dot.Content, "\\nline") || !strings.Contains(dot.Content, "back\\\\slash") {
		t.Fatalf("DOT content missing escaped label text:\n%s", dot.Content)
	}
}
