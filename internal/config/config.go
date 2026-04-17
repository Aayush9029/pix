package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const EnvAPIKey = "OPENAI_API_KEY"

func APIKey() string {
	return strings.TrimSpace(os.Getenv(EnvAPIKey))
}

func ResolveOutputDir(dir string) (string, error) {
	if dir == "" {
		dir = "."
	}
	if strings.HasPrefix(dir, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, strings.TrimPrefix(dir, "~"))
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", fmt.Errorf("create output dir %s: %w", abs, err)
	}
	return abs, nil
}
