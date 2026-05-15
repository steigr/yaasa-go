package desk

import (
	"fmt"
	"time"
)

func (d *Desk) verboseLogf(format string, args ...any) {
	if !d.verbose {
		return
	}
	verboseLogf(format, args...)
}

func verboseLogf(format string, args ...any) {
	now := time.Now()
	prefix := fmt.Sprintf("[unix=%d unix_ms=%d] ", now.Unix(), now.UnixMilli())
	fmt.Printf(prefix+format+"\n", args...)
}
