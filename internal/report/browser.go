// Package report renders saved report definitions to PDF.
//
// The PDF is the print route of the SPA, loaded in a headless Chromium-family
// browser and printed to paper. That is a deliberate choice: it means the
// charts in a report are the exact charts the app draws on screen, down to the
// palette and the confidence bands, rather than a second implementation that
// would drift out of step with the first.
package report

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// ErrNoBrowser is returned when no Chromium-family browser could be found. It
// is distinct so the API can turn it into a clear message: reporting is the one
// feature that needs a browser, and the rest of the server runs without one.
var ErrNoBrowser = errors.New("no Chromium-family browser found for PDF export")

// FindBrowser locates the executable used to print PDFs.
//
// An explicit configured path wins. Otherwise it prefers the Edge that ships
// with Windows Server 2019 and later, so a standard install needs nothing
// added; on other platforms it falls back to the usual Chrome and Chromium
// locations, which is what makes the feature testable off the target OS.
func FindBrowser(configured string) (string, error) {
	if configured != "" {
		if fileExists(configured) {
			return configured, nil
		}
		return "", ErrNoBrowser
	}

	for _, candidate := range candidatePaths() {
		if fileExists(candidate) {
			return candidate, nil
		}
	}

	// A browser on PATH is the last resort, so a container image that installs
	// chromium into a nonstandard prefix still works.
	for _, name := range []string{"msedge", "chrome", "chromium", "chromium-browser", "google-chrome"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}

	return "", ErrNoBrowser
}

func candidatePaths() []string {
	switch runtime.GOOS {
	case "windows":
		var paths []string
		// Edge is installed per-machine under Program Files; the x86 path is
		// where it lands on 64-bit Windows, Server included.
		for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LOCALAPPDATA"} {
			root := os.Getenv(env)
			if root == "" {
				continue
			}
			paths = append(paths,
				filepath.Join(root, "Microsoft", "Edge", "Application", "msedge.exe"),
				filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe"),
			)
		}
		return paths

	case "darwin":
		return []string{
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}

	default:
		return []string{
			"/usr/bin/microsoft-edge",
			"/usr/bin/google-chrome",
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
			"/snap/bin/chromium",
		}
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
