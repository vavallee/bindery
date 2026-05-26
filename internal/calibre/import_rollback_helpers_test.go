package calibre

import "os"

func writeRollbackTestFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

func rollbackTestFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
