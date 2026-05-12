package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/realxen/cartograph/internal/service"
)

func testPackageMapResult() *service.PackageMapResult {
	return &service.PackageMapResult{
		Repo: "cartograph",
		Packages: []service.PackageMapPackage{
			{Path: "cmd", FileCount: 2},
			{Path: "pkg/quote\"", FileCount: 1},
		},
		Imports: []service.PackageMapImport{{
			From:            "cmd",
			To:              "pkg/quote\"",
			Count:           3,
			SourceFileCount: 2,
		}},
		Summary: service.PackageMapSummary{
			TotalEdges:   1,
			ShownEdges:   1,
			TotalImports: 3,
			ShownImports: 3,
			PackageCount: 2,
		},
		Content: "flowchart LR\n  pkg0[\"cmd\"]\n  pkg1[\"pkg/quote&quot;\"]\n  pkg0 -->|\"3\"| pkg1\n",
	}
}

func TestFormatPackageMapJSON(t *testing.T) {
	out, err := formatPackageMapOutput(testPackageMapResult(), "json")
	if err != nil {
		t.Fatalf("format json: %v", err)
	}
	var result service.PackageMapResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal json output: %v", err)
	}
	if result.Repo != "cartograph" || len(result.Imports) != 1 {
		t.Fatalf("unexpected JSON output: %#v", result)
	}
}

func TestFormatPackageMapMermaid(t *testing.T) {
	out, err := formatPackageMapOutput(testPackageMapResult(), "mermaid")
	if err != nil {
		t.Fatalf("format mermaid: %v", err)
	}
	if !strings.HasPrefix(out, "flowchart LR\n") {
		t.Fatalf("expected Mermaid content, got %q", out)
	}
	if strings.Contains(out, "cmd -->") {
		t.Fatal("Mermaid output should use generated node IDs")
	}
}

func TestFormatPackageMapDOT(t *testing.T) {
	result := testPackageMapResult()
	result.Content = "digraph PackageMap {\n  rankdir=LR;\n  pkg0 [label=\"cmd\"];\n}\n"
	out, err := formatPackageMapOutput(result, "dot")
	if err != nil {
		t.Fatalf("format dot: %v", err)
	}
	if !strings.HasPrefix(out, "digraph PackageMap {\n") {
		t.Fatalf("expected DOT content, got %q", out)
	}
}

func TestFormatPackageMapInvalidFormat(t *testing.T) {
	if _, err := formatPackageMapOutput(testPackageMapResult(), "svg"); err == nil {
		t.Fatal("expected invalid format error")
	}
}
