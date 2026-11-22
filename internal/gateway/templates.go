package gateway

import (
	"embed"
	"fmt"
	"path"
)

// templatesFS is compiled into the gateway binary so a deployment always has
// a known-good base configuration, even when no operator files are mounted.
//
//go:embed templates/tiku-adapter.conf templates/tiku-proxy.inc
var templatesFS embed.FS

func embeddedTemplate(name string) ([]byte, error) {
	data, err := templatesFS.ReadFile(path.Join("templates", name))
	if err != nil {
		return nil, fmt.Errorf("read embedded Nginx template %s: %w", name, err)
	}
	return data, nil
}
