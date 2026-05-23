// scratch plugin: download, verify, extract, and register toolchain archives.
//
// Port of the original Babashka plugin (plugin.bb) to Go using sdk/muplugin.
// Output bytes match the bb version action-for-action so existing scenarios
// continue to pass.
package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/chau/mu/sdk/muplugin"
)

func main() {
	(&muplugin.Plugin{
		Name:        "scratch",
		Version:     "0.1.0",
		Description: "Download, verify, and extract toolchain archives",
		Produces:    []string{"toolchain"},
		Plan:        plan,
	}).Main()
}

func plan(_ context.Context, req muplugin.PlanRequest) (muplugin.PlanResponse, error) {
	cfg := req.Target.Config
	url, _ := cfg["url"].(string)
	sha256, _ := cfg["sha256"].(string)
	version, _ := cfg["version"].(string)
	strip, _ := cfg["strip_prefix"].(string)

	tgtName := req.Target.Name
	if tgtName == "" {
		tgtName = "tool"
	}
	shortName := lastSegment(tgtName)

	outDir := fmt.Sprintf("toolchains/%s/%s", shortName, version)
	archive := fmt.Sprintf("%s/download/%s", outDir, lastSegment(url))
	binDir := outDir + "/bin"
	manifest := outDir + "/manifest.json"

	fetchCmd := fmt.Sprintf(
		"mkdir -p $(dirname '%s') && "+
			"curl -fsSL '%s' -o '%s' && "+
			"echo '%s  %s' | "+
			"if command -v sha256sum >/dev/null 2>&1; then sha256sum -c; else shasum -a 256 -c; fi",
		archive, url, archive, sha256, archive,
	)

	stripFlag := ""
	if strip != "" {
		stripFlag = "--strip-components=1 "
	}
	var extractInner string
	switch {
	case strings.HasSuffix(url, ".tar.gz"), strings.HasSuffix(url, ".tgz"):
		extractInner = fmt.Sprintf("tar xzf '%s' -C '%s' %s", archive, binDir, stripFlag)
	case strings.HasSuffix(url, ".zip"):
		extractInner = fmt.Sprintf("unzip -o '%s' -d '%s'", archive, binDir)
	default:
		extractInner = fmt.Sprintf("cp '%s' '%s/%s' && chmod +x '%s/%s'",
			archive, binDir, shortName, binDir, shortName)
	}
	extractCmd := fmt.Sprintf("mkdir -p '%s' && %s", binDir, extractInner)

	verifyCmd := fmt.Sprintf("'%s/%s' --version", binDir, shortName)

	manifestJSON := fmt.Sprintf(`{
  "bin_dir" : "%s",
  "name" : "%s",
  "sha256" : "%s",
  "version" : "%s"
}`, binDir, shortName, sha256, version)
	registerCmd := fmt.Sprintf("cat > '%s' <<'MANIFEST_EOF'\n%s\nMANIFEST_EOF", manifest, manifestJSON)

	return muplugin.PlanResponse{
		Actions: []muplugin.ActionSpec{
			{
				ID:        shortName + "/fetch",
				Command:   []string{"sh", "-c", fetchCmd},
				Network:   true,
				DependsOn: []string{},
				Inputs:    map[string]string{},
				Outputs:   []string{archive},
			},
			{
				ID:        shortName + "/extract",
				Command:   []string{"sh", "-c", extractCmd},
				DependsOn: []string{shortName + "/fetch"},
				Inputs:    map[string]string{},
				Outputs:   []string{binDir + "/" + shortName},
			},
			{
				ID:        shortName + "/verify",
				Command:   []string{"sh", "-c", verifyCmd},
				DependsOn: []string{shortName + "/extract"},
				Inputs:    map[string]string{},
				Outputs:   []string{},
			},
			{
				ID:        shortName + "/register",
				Command:   []string{"sh", "-c", registerCmd},
				DependsOn: []string{shortName + "/verify"},
				Inputs:    map[string]string{},
				Outputs:   []string{manifest},
			},
		},
		Outputs: map[string]string{
			"toolchain_bin": binDir,
			"manifest":      manifest,
		},
	}, nil
}

func lastSegment(s string) string {
	// Match the bb plugin's split on either ':' or '/'.
	last := s
	for _, sep := range []string{":", "/"} {
		if i := strings.LastIndex(last, sep); i >= 0 {
			last = last[i+1:]
		}
	}
	return last
}
