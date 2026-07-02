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

package driverconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"sigs.k8s.io/yaml"
)

// buildConfMap loads the config file at filePath, validates and strips
// "apiVersion", and returns the resulting map.
func buildConfMap(filePath string) (map[string]any, error) {
	confMap, err := loadFile(filePath)
	if err != nil {
		return nil, err
	}

	if err := validateAPIVersion(confMap); err != nil {
		return nil, err
	}
	delete(confMap, "apiVersion")

	return confMap, nil
}

// loadFile reads and parses the YAML file at path into a map.
func loadFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	confMap := map[string]any{}
	if err := yaml.Unmarshal(data, &confMap); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	return confMap, nil
}

// validateAPIVersion checks confMap["apiVersion"] when present.
func validateAPIVersion(confMap map[string]any) error {
	raw, ok := confMap["apiVersion"]
	if !ok {
		return nil
	}
	apiVer, _ := raw.(string)
	if apiVer != ConfigAPIVersion {
		return fmt.Errorf("unsupported apiVersion %q, want %q", apiVer, ConfigAPIVersion)
	}
	return nil
}

// applyMap applies only the keys present in m to cfg; absent keys are
// untouched (encoding/json.Unmarshal semantics). Unknown keys are rejected
// to catch typos early rather than silently ignoring them.
func applyMap(cfg *Config, m map[string]any) error {
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshaling config map: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return fmt.Errorf("applying config map: %w", err)
	}
	return nil
}
