/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/driverconfig"
)

// TestRunUsesConfigSysFSOverlay: run() must build the sysfs overlay from cfg, not the pre-merge driverFlags.
func TestRunUsesConfigSysFSOverlay(t *testing.T) {
	cfg := driverconfig.Default()
	cfg.SysFSOverlay = filepath.Join(t.TempDir(), "does-not-exist.yaml")

	err := run(testr.New(t), cfg)
	if err == nil {
		t.Fatal("run() succeeded, want error reading missing sysfs overlay")
	}
	if !strings.Contains(err.Error(), "read sysfs overlay") {
		t.Fatalf("run() error = %v, want sysfs overlay read error (cfg.SysFSOverlay was ignored)", err)
	}
}
