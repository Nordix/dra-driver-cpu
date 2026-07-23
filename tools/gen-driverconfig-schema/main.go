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

// Command gen-driverconfig-schema regenerates
// deployment/helm/dra-driver-cpu/driverconfig.schema.json from the
// driverconfig.Config Go struct. Run via 'make driverconfig-schema'.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/kubernetes-sigs/dra-driver-cpu/internal/driverconfig"
)

func main() {
	out := flag.String("out", "deployment/helm/dra-driver-cpu/driverconfig.schema.json", "path to write the generated schema to")
	flag.Parse()

	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, "gen-driverconfig-schema:", err)
		os.Exit(1)
	}
}

func run(out string) error {
	schema, err := driverconfig.GenerateDriverConfigSchema()
	if err != nil {
		return fmt.Errorf("generating schema: %w", err)
	}
	if err := os.WriteFile(out, schema, 0600); err != nil {
		return fmt.Errorf("writing %s: %w", out, err)
	}
	return nil
}
