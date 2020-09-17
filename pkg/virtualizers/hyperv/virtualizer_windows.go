// +build windows

/**
 * SPDX-License-Identifier: Apache-2.0
 * Copyright 2020 vorteil.io Pty Ltd
 */

package hyperv

import (
	"fmt"
	"io"

	"github.com/natefinch/npipe"
)

// initLogs setups the ability to read from the pipe
func (v *Virtualizer) initLogs() error {
	conn, err := npipe.Dial(fmt.Sprintf("\\\\.\\pipe\\%s", v.id))
	if err != nil {
		v.log("error", "Error Dialing Pipe: %v", err)
		return err
	}
	v.sock = conn

	go io.Copy(v.serialLogger, v.sock)
	return nil
}
