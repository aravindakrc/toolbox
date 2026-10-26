// Package platform describes the machine tbx is running on.
package platform

import "runtime"

// Info identifies an operating system and CPU architecture using Go's names
// (for example "windows"/"amd64").
type Info struct {
	OS   string
	Arch string
}

// Current returns the platform of the running process.
func Current() Info {
	return Info{OS: runtime.GOOS, Arch: runtime.GOARCH}
}
