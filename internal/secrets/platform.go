package secrets

import "runtime"

func isWindows() bool { return runtime.GOOS == "windows" }
func isMac() bool     { return runtime.GOOS == "darwin" }
