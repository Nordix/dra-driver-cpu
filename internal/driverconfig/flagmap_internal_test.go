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

// Internal package test: has direct access to the unexported flagToJSONKey map.
package driverconfig

import (
	"flag"
	"testing"
)

// TestFlagToJSONKey_CoversAllFlags: every AddFlags flag has a flagToJSONKey entry.
func TestFlagToJSONKey_CoversAllFlags(t *testing.T) {
	cfg := Config{}
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg.AddFlags(fs)

	fs.VisitAll(func(f *flag.Flag) {
		if _, ok := flagToJSONKey[f.Name]; !ok {
			t.Errorf("flag %q is registered via AddFlags but missing from flagToJSONKey", f.Name)
		}
	})
}

// TestFlagToJSONKey_NoStaleEntries: every flagToJSONKey entry maps to a real flag.
func TestFlagToJSONKey_NoStaleEntries(t *testing.T) {
	cfg := Config{}
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg.AddFlags(fs)

	for flagName := range flagToJSONKey {
		if fs.Lookup(flagName) == nil {
			t.Errorf("flagToJSONKey has entry %q but AddFlags does not register this flag", flagName)
		}
	}
}
