package utils

import (
	"os"
	"path/filepath"
)

// CreateFolder creates folders if they don't exist
func CreateFolder(paths ...string) error {
	for _, p := range paths {
		if err := os.MkdirAll(p, os.ModePerm); err != nil {
			return err
		}
	}
	return nil
}

// FileExists checks if a file exists
func FileExists(path string) bool {
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}

// GetFileExtension returns the file extension without the dot
func GetFileExtension(filename string) string {
	ext := filepath.Ext(filename)
	if len(ext) > 0 {
		return ext[1:]
	}
	return ""
}
