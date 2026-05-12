package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/realxen/cartograph/internal/service"
)

func formatPackageMapOutput(result *service.PackageMapResult, format string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "json":
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return "", fmt.Errorf("JSON marshal: %w", err)
		}
		return string(data) + "\n", nil
	case "mermaid", "dot":
		return result.Content, nil
	default:
		return "", fmt.Errorf("unsupported format %q", format)
	}
}
