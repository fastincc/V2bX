//go:build !linux && !darwin

package trafficbatch

import (
	"fmt"
	"os"
)

func lockFile(*os.File) error { return fmt.Errorf("batch delivery requires Linux or macOS") }
