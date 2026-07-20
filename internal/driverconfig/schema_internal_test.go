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

// Internal package test: has direct access to the unexported schema metadata.
package driverconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// TestGenerateDriverConfigSchema_CoversAllFields: every Config json field is
// either excluded on purpose (schemaExcludedFields) or present in the
// generated schema's properties.
func TestGenerateDriverConfigSchema_CoversAllFields(t *testing.T) {
	out, err := GenerateDriverConfigSchema()
	if err != nil {
		t.Fatalf("GenerateDriverConfigSchema() error: %v", err)
	}

	var doc struct {
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("unmarshaling generated schema: %v", err)
	}

	typ := reflect.TypeFor[Config]()
	for field := range typ.Fields() {
		jsonName, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if jsonName == "" || jsonName == "-" {
			continue
		}
		_, present := doc.Properties[jsonName]
		if schemaExcludedFields[jsonName] {
			if present {
				t.Errorf("Config field %q (json key %q) is marked excluded but appears in the generated schema", field.Name, jsonName)
			}
			continue
		}
		if !present {
			t.Errorf("Config field %q (json key %q) is missing from the generated schema", field.Name, jsonName)
		}
	}
}

// TestGenerateDriverConfigSchema_MatchesCheckedInFile: the checked-in
// driverconfig.schema.json must match the generator's output; run 'make
// driverconfig-schema' to regenerate it after changing Config.
func TestGenerateDriverConfigSchema_MatchesCheckedInFile(t *testing.T) {
	want, err := GenerateDriverConfigSchema()
	if err != nil {
		t.Fatalf("GenerateDriverConfigSchema() error: %v", err)
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	schemaPath := filepath.Join(filepath.Dir(thisFile), "..", "..", "deployment", "helm", "dra-driver-cpu", "driverconfig.schema.json")

	got, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("reading %s: %v", schemaPath, err)
	}

	if string(got) != string(want) {
		t.Errorf("%s is out of date; run 'make driverconfig-schema' to regenerate it.\ngot:\n%s\nwant:\n%s", schemaPath, got, want)
	}
}
