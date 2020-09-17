// +build linux darwin

/**
 * SPDX-License-Identifier: Apache-2.0
 * Copyright 2020 vorteil.io Pty Ltd
 */

package hyperv

import "errors"

// initLogs returns an error as this is a windows only hypervisor
func (v *Virtualizer) initLogs() error {
	return errors.New("hyperv is not implemented on this operating system.. how did you get here?")
}
