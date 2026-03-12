/*
Copyright 2026 The Kubernetes Authors.

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
	"bufio"
	"bytes"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
	tomlv2 "github.com/pelletier/go-toml/v2"
)

const (
	containerdConfigFile = "/etc/containerd/config.toml"
	crioConfigFile       = "/etc/crio/crio.conf.d/10-nri-enabler.conf"
	nriKey               = "io.containerd.nri.v1.nri"
	containerdUnit       = "containerd.service"
	crioUnit             = "crio.service"

	// crioRestartDelay gives CRI-O time to settle before we query it post-restart.
	crioRestartDelay = 3 * time.Second
)

func main() {
	runtime := flag.String("runtime", "", `container runtime to configure: "containerd" or "crio"`)
	flag.Parse()

	var unit string
	switch *runtime {
	case "containerd":
		unit = containerdUnit
	case "crio":
		unit = crioUnit
	default:
		log.Fatalf("unknown runtime %q; must be \"containerd\" or \"crio\"", *runtime)
	}

	ctx := context.Background()

	conn, err := dbus.NewSystemConnectionContext(ctx)
	if err != nil {
		log.Fatalf("failed to connect to D-Bus: %v", err)
	}
	defer conn.Close()

	if err := configureAndRestartRuntime(ctx, unit, conn); err != nil {
		log.Fatalf("failed to configure NRI: %v", err)
	}

	log.Printf("successfully enabled NRI for %s", unit)
}

// configureAndRestartRuntime enables NRI for the given container runtime and
// restarts its systemd unit.
func configureAndRestartRuntime(ctx context.Context, unit string, conn *dbus.Conn) error {
	switch unit {
	case containerdUnit:
		if err := enableNRIForContainerd(); err != nil {
			return fmt.Errorf("enabling NRI for containerd: %w", err)
		}
	case crioUnit:
		if err := enableNRIForCrio(); err != nil {
			return fmt.Errorf("enabling NRI for CRI-O: %w", err)
		}
		// CRI-O needs a brief pause before it is ready to be queried post-restart.
		log.Printf("waiting %s for CRI-O to settle...", crioRestartDelay)
		select {
		case <-time.After(crioRestartDelay):
		case <-ctx.Done():
			return fmt.Errorf("interrupted while waiting for CRI-O: %w", ctx.Err())
		}
	default:
		return fmt.Errorf("unknown container runtime unit %q", unit)
	}

	return restartSystemdUnit(ctx, conn, unit)
}

// enableNRIForContainerd reads the containerd TOML config, enables the NRI
// plugin section, and writes it back.
func enableNRIForContainerd() error {
	log.Printf("configuring NRI for containerd...")

	tomlMap, err := readTOMLConfig(containerdConfigFile)
	if err != nil {
		return fmt.Errorf("reading config %q: %w", containerdConfigFile, err)
	}

	applyNRIConfig(tomlMap)

	if err := writeTOMLConfig(containerdConfigFile, tomlMap); err != nil {
		return fmt.Errorf("writing config %q: %w", containerdConfigFile, err)
	}
	return nil
}

// enableNRIForCrio writes a CRI-O drop-in config file that enables NRI.
func enableNRIForCrio() (retErr error) {
	log.Printf("configuring NRI for CRI-O...")

	f, err := os.Create(crioConfigFile)
	if err != nil {
		return fmt.Errorf("creating drop-in file %q: %w", crioConfigFile, err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && retErr == nil {
			retErr = fmt.Errorf("closing drop-in file %q: %w", crioConfigFile, cerr)
		}
	}()

	w := bufio.NewWriter(f)
	if _, err := fmt.Fprintln(w, "[crio.nri]\nenable_nri = true"); err != nil {
		return fmt.Errorf("writing CRI-O config: %w", err)
	}
	return w.Flush()
}

// applyNRIConfig mutates tomlMap in-place to enable the NRI plugin.
func applyNRIConfig(tomlMap map[string]any) {
	plugins, ok := tomlMap["plugins"].(map[string]any)
	if !ok {
		log.Printf("top-level [plugins] section not found; creating it")
		plugins = make(map[string]any)
		tomlMap["plugins"] = plugins
	}

	nri, ok := plugins[nriKey].(map[string]any)
	if !ok {
		log.Printf("[plugins.%q] section not found; creating it", nriKey)
		nri = make(map[string]any)
		plugins[nriKey] = nri
	}

	nri["disable"] = false
}

// readTOMLConfig reads and unmarshals a TOML file into a generic map.
func readTOMLConfig(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading file: %w", err)
	}

	m := make(map[string]any)
	if err := tomlv2.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("unmarshaling TOML: %w", err)
	}
	return m, nil
}

// writeTOMLConfig encodes config as TOML and overwrites path. The encode step
// happens before truncation, so a failed encode leaves the original intact.
func writeTOMLConfig(path string, config map[string]any) (retErr error) {
	var buf bytes.Buffer
	enc := tomlv2.NewEncoder(&buf)
	enc.SetIndentTables(true)
	if err := enc.Encode(config); err != nil {
		return fmt.Errorf("encoding TOML: %w", err)
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("opening file for writing: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && retErr == nil {
			retErr = fmt.Errorf("closing file %q: %w", path, cerr)
		}
	}()

	if _, err := buf.WriteTo(f); err != nil {
		return fmt.Errorf("writing file: %w", err)
	}
	return nil
}

// restartSystemdUnit asks systemd to restart unit and waits for the result.
func restartSystemdUnit(ctx context.Context, conn *dbus.Conn, unit string) error {
	log.Printf("restarting %s...", unit)

	resC := make(chan string, 1)
	if _, err := conn.RestartUnitContext(ctx, unit, "replace", resC); err != nil {
		return fmt.Errorf("sending restart request for %q: %w", unit, err)
	}

	select {
	case result := <-resC:
		if result != "done" {
			return fmt.Errorf("restart of %q completed with unexpected result %q", unit, result)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("interrupted while waiting for %q to restart: %w", unit, ctx.Err())
	}
}
