package importer

import "os"

// writeFile stores a small generated file alongside a run's artifacts.
func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o640)
}
