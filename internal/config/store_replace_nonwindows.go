//go:build !windows

package config

import "os"

func replaceConfigTarget(source, target, _ string) error {
	return os.Rename(source, target)
}
