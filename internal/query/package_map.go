package query

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/cloudprivacylabs/lpg/v2"

	"github.com/realxen/cartograph/internal/graph"
	"github.com/realxen/cartograph/internal/ingestion"
	"github.com/realxen/cartograph/internal/service"
)

const (
	defaultPackageMapJSONLimit    = 500
	defaultPackageMapDiagramLimit = 100
	maxPackageMapLimit            = 10000
	maxPackageMapFilesPerEdge     = 5
)

type packageEdgeKey struct {
	from string
	to   string
}

type filePairKey struct {
	fromFile string
	toFile   string
}

type packageAggregate struct {
	files map[string]struct{}
}

type aggregateEdge struct {
	from        string
	to          string
	count       int
	sourceFiles map[string]struct{}
	filePairs   []service.PackageMapFileImport
}

type packageMapBuildResult struct {
	packages           []service.PackageMapPackage
	imports            []service.PackageMapImport
	totalImports       int
	skippedSelfImports int
	skippedTestImports int
}

// PackageMap aggregates resolved internal File->File IMPORTS edges into a
// package/folder-level import map. It does not mutate the graph.
func (b *Backend) PackageMap(req service.PackageMapRequest) (*service.PackageMapResult, error) {
	format := service.NormalizePackageMapFormat(req.Format)
	if format == "" {
		return nil, fmt.Errorf("unsupported package map format %q", req.Format)
	}

	limit := req.Limit
	if limit <= 0 {
		if format == service.PackageMapFormatMermaid || format == service.PackageMapFormatDOT {
			limit = defaultPackageMapDiagramLimit
		} else {
			limit = defaultPackageMapJSONLimit
		}
	}
	if limit > maxPackageMapLimit {
		limit = maxPackageMapLimit
	}

	minCount := req.MinCount
	if minCount <= 0 {
		minCount = 1
	}

	build := buildPackageMap(b.Graph, req.IncludeTests, req.IncludeFiles, minCount)
	totalEdges := len(build.imports)
	totalImports := build.totalImports

	imports := build.imports
	truncated := false
	if limit < len(imports) {
		imports = imports[:limit]
		truncated = true
	}

	shownImports := 0
	for _, imp := range imports {
		shownImports += imp.Count
	}

	result := &service.PackageMapResult{
		Repo:     req.Repo,
		Packages: build.packages,
		Imports:  imports,
		Summary: service.PackageMapSummary{
			TotalEdges:         totalEdges,
			ShownEdges:         len(imports),
			TotalImports:       totalImports,
			ShownImports:       shownImports,
			PackageCount:       len(build.packages),
			SkippedSelfImports: build.skippedSelfImports,
			SkippedTestImports: build.skippedTestImports,
			Truncated:          truncated,
		},
	}

	switch format {
	case service.PackageMapFormatMermaid:
		result.Content = renderPackageMapMermaid(result)
	case service.PackageMapFormatDOT:
		result.Content = renderPackageMapDOT(result)
	}
	return result, nil
}

func buildPackageMap(g *lpg.Graph, includeTests, includeFiles bool, minCount int) packageMapBuildResult {
	if g == nil {
		return packageMapBuildResult{}
	}

	type nodePath struct {
		filePath string
		pkgPath  string
		ok       bool
	}

	nodePaths := make(map[*lpg.Node]nodePath)
	packages := make(map[string]*packageAggregate)
	edges := make(map[packageEdgeKey]*aggregateEdge)
	seenFilePairs := make(map[filePairKey]struct{})
	skippedSelfImports := 0
	skippedTestImports := 0

	getNodePath := func(node *lpg.Node) nodePath {
		if cached, ok := nodePaths[node]; ok {
			return cached
		}
		fp := normalizeGraphFilePath(graph.GetStringProp(node, graph.PropFilePath))
		np := nodePath{filePath: fp}
		if fp != "" {
			np.pkgPath = packagePathForFile(fp)
			np.ok = true
		}
		nodePaths[node] = np
		return np
	}

	graph.ForEachEdge(g, func(edge *lpg.Edge) bool {
		if !isPackageMapFileImportEdge(edge) {
			return true
		}

		fromNode := edge.GetFrom()
		toNode := edge.GetTo()
		fromPath := getNodePath(fromNode)
		toPath := getNodePath(toNode)
		if !fromPath.ok || !toPath.ok {
			return true
		}
		if !includeTests && (ingestion.IsUsageFile(fromPath.filePath) || ingestion.IsUsageFile(toPath.filePath)) {
			skippedTestImports++
			return true
		}
		if fromPath.pkgPath == toPath.pkgPath {
			skippedSelfImports++
			return true
		}

		fileKey := filePairKey{fromFile: fromPath.filePath, toFile: toPath.filePath}
		if _, seen := seenFilePairs[fileKey]; seen {
			return true
		}
		seenFilePairs[fileKey] = struct{}{}

		ensurePackage(packages, fromPath.pkgPath).files[fromPath.filePath] = struct{}{}
		ensurePackage(packages, toPath.pkgPath).files[toPath.filePath] = struct{}{}

		edgeKey := packageEdgeKey{from: fromPath.pkgPath, to: toPath.pkgPath}
		agg := edges[edgeKey]
		if agg == nil {
			agg = &aggregateEdge{
				from:        fromPath.pkgPath,
				to:          toPath.pkgPath,
				sourceFiles: make(map[string]struct{}),
			}
			edges[edgeKey] = agg
		}
		agg.count++
		agg.sourceFiles[fromPath.filePath] = struct{}{}
		if includeFiles {
			addBoundedPackageMapFilePair(agg, service.PackageMapFileImport{
				FromFile: fromPath.filePath,
				ToFile:   toPath.filePath,
			})
		}
		return true
	})

	imports := make([]service.PackageMapImport, 0, len(edges))
	totalImports := 0
	includedPackages := make(map[string]struct{})
	for _, edge := range edges {
		if edge.count < minCount {
			continue
		}
		imp := service.PackageMapImport{
			From:            edge.from,
			To:              edge.to,
			Count:           edge.count,
			SourceFileCount: len(edge.sourceFiles),
		}
		if includeFiles {
			imp.Files = append(imp.Files, edge.filePairs...)
			imp.FilesTruncated = edge.count > len(edge.filePairs)
		}
		imports = append(imports, imp)
		totalImports += edge.count
		includedPackages[edge.from] = struct{}{}
		includedPackages[edge.to] = struct{}{}
	}
	sortPackageMapImports(imports)

	pkgResults := make([]service.PackageMapPackage, 0, len(includedPackages))
	for pkgPath := range includedPackages {
		agg := packages[pkgPath]
		fileCount := 0
		if agg != nil {
			fileCount = len(agg.files)
		}
		pkgResults = append(pkgResults, service.PackageMapPackage{
			Path:      pkgPath,
			FileCount: fileCount,
		})
	}
	sort.Slice(pkgResults, func(i, j int) bool {
		return pkgResults[i].Path < pkgResults[j].Path
	})

	return packageMapBuildResult{
		packages:           pkgResults,
		imports:            imports,
		totalImports:       totalImports,
		skippedSelfImports: skippedSelfImports,
		skippedTestImports: skippedTestImports,
	}
}

func ensurePackage(packages map[string]*packageAggregate, pkgPath string) *packageAggregate {
	pkg := packages[pkgPath]
	if pkg == nil {
		pkg = &packageAggregate{files: make(map[string]struct{})}
		packages[pkgPath] = pkg
	}
	return pkg
}

func addBoundedPackageMapFilePair(edge *aggregateEdge, pair service.PackageMapFileImport) {
	insertAt := sort.Search(len(edge.filePairs), func(i int) bool {
		if edge.filePairs[i].FromFile != pair.FromFile {
			return edge.filePairs[i].FromFile >= pair.FromFile
		}
		return edge.filePairs[i].ToFile >= pair.ToFile
	})
	if insertAt < len(edge.filePairs) && edge.filePairs[insertAt] == pair {
		return
	}
	if insertAt >= maxPackageMapFilesPerEdge {
		return
	}
	edge.filePairs = append(edge.filePairs, service.PackageMapFileImport{})
	copy(edge.filePairs[insertAt+1:], edge.filePairs[insertAt:])
	edge.filePairs[insertAt] = pair
	if len(edge.filePairs) > maxPackageMapFilesPerEdge {
		edge.filePairs = edge.filePairs[:maxPackageMapFilesPerEdge]
	}
}

func normalizeGraphFilePath(filePath string) string {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return ""
	}
	cleaned := path.Clean(filepath.ToSlash(filePath))
	if cleaned == "/" || cleaned == "." {
		return ""
	}
	return strings.TrimPrefix(cleaned, "./")
}

func packagePathForFile(filePath string) string {
	dir := path.Dir(filePath)
	if dir == "" || dir == "." || dir == "/" {
		return "."
	}
	return strings.TrimSuffix(dir, "/")
}

func isPackageMapFileImportEdge(edge *lpg.Edge) bool {
	rel, err := graph.GetEdgeRelType(edge)
	if err != nil || rel != graph.RelImports {
		return false
	}
	return edge.GetFrom().HasLabel(string(graph.LabelFile)) && edge.GetTo().HasLabel(string(graph.LabelFile))
}

func sortPackageMapImports(imports []service.PackageMapImport) {
	sort.Slice(imports, func(i, j int) bool {
		if imports[i].Count != imports[j].Count {
			return imports[i].Count > imports[j].Count
		}
		if imports[i].From != imports[j].From {
			return imports[i].From < imports[j].From
		}
		return imports[i].To < imports[j].To
	})
}

func renderPackageMapMermaid(result *service.PackageMapResult) string {
	var b strings.Builder
	b.WriteString("flowchart LR\n")
	if result == nil {
		return b.String()
	}
	nodeIDs := packageMapNodeIDs(result.Imports)
	paths := packageMapNodePaths(nodeIDs)
	for _, pkgPath := range paths {
		b.WriteString("  ")
		b.WriteString(nodeIDs[pkgPath])
		b.WriteString("[\"")
		b.WriteString(escapeMermaidLabel(pkgPath))
		b.WriteString("\"]\n")
	}
	for _, imp := range result.Imports {
		b.WriteString("  ")
		b.WriteString(nodeIDs[imp.From])
		b.WriteString(" -->|\"")
		b.WriteString(strconv.Itoa(imp.Count))
		b.WriteString("\"| ")
		b.WriteString(nodeIDs[imp.To])
		b.WriteString("\n")
	}
	if result.Summary.Truncated {
		b.WriteString("  %% truncated: showing ")
		b.WriteString(strconv.Itoa(result.Summary.ShownEdges))
		b.WriteString(" of ")
		b.WriteString(strconv.Itoa(result.Summary.TotalEdges))
		b.WriteString(" package imports\n")
	}
	return b.String()
}

func renderPackageMapDOT(result *service.PackageMapResult) string {
	var b strings.Builder
	b.WriteString("digraph PackageMap {\n")
	b.WriteString("  rankdir=LR;\n")
	if result != nil {
		nodeIDs := packageMapNodeIDs(result.Imports)
		paths := packageMapNodePaths(nodeIDs)
		for _, pkgPath := range paths {
			b.WriteString("  ")
			b.WriteString(nodeIDs[pkgPath])
			b.WriteString(" [label=\"")
			b.WriteString(escapeDOTLabel(pkgPath))
			b.WriteString("\"];\n")
		}
		for _, imp := range result.Imports {
			b.WriteString("  ")
			b.WriteString(nodeIDs[imp.From])
			b.WriteString(" -> ")
			b.WriteString(nodeIDs[imp.To])
			b.WriteString(" [label=\"")
			b.WriteString(strconv.Itoa(imp.Count))
			b.WriteString("\"];\n")
		}
		if result.Summary.Truncated {
			b.WriteString("  // truncated: showing ")
			b.WriteString(strconv.Itoa(result.Summary.ShownEdges))
			b.WriteString(" of ")
			b.WriteString(strconv.Itoa(result.Summary.TotalEdges))
			b.WriteString(" package imports\n")
		}
	}
	b.WriteString("}\n")
	return b.String()
}

func packageMapNodeIDs(imports []service.PackageMapImport) map[string]string {
	seen := make(map[string]struct{})
	for _, imp := range imports {
		seen[imp.From] = struct{}{}
		seen[imp.To] = struct{}{}
	}
	paths := make([]string, 0, len(seen))
	for pkgPath := range seen {
		paths = append(paths, pkgPath)
	}
	sort.Strings(paths)
	nodeIDs := make(map[string]string, len(paths))
	for i, pkgPath := range paths {
		nodeIDs[pkgPath] = fmt.Sprintf("pkg%d", i)
	}
	return nodeIDs
}

func packageMapNodePaths(nodeIDs map[string]string) []string {
	paths := make([]string, 0, len(nodeIDs))
	for pkgPath := range nodeIDs {
		paths = append(paths, pkgPath)
	}
	sort.Strings(paths)
	return paths
}

func escapeMermaidLabel(label string) string {
	var b strings.Builder
	for _, r := range label {
		if unicode.IsControl(r) {
			b.WriteByte(' ')
			continue
		}
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&#39;")
		case '[':
			b.WriteString("&#91;")
		case ']':
			b.WriteString("&#93;")
		case '|':
			b.WriteString("&#124;")
		default:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func escapeDOTLabel(label string) string {
	var b strings.Builder
	for _, r := range label {
		switch r {
		case '\\':
			b.WriteString("\\\\")
		case '"':
			b.WriteString("\\\"")
		case '\n', '\r':
			b.WriteString("\\n")
		case '\t':
			b.WriteByte(' ')
		default:
			if unicode.IsControl(r) {
				b.WriteByte(' ')
			} else {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}
