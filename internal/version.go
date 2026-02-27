package internal

import (
	"fmt"
	"runtime"
)

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

func RunVersion() {
	fmt.Printf("orchestra %s (%s/%s, commit %s, built %s)\n", Version, runtime.GOOS, runtime.GOARCH, Commit, Date)
}
