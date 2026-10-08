/*
Copyright 2026.

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

// Command contractlabels stamps the Cluster API contract labels onto the
// generated CRDs.
//
// Cluster API discovers which contract a provider speaks from a label on the
// CRD, not from the CRD's version:
//
//	cluster.x-k8s.io/<contract-version>: <served-api-versions>
//
// The value lists the apiVersions the CRD actually serves, joined by "_". Hard
// coding that pair in a kustomization means keeping two unrelated things in
// agreement by hand. Here both are DERIVED:
//
//   - the contract version(s) from the Cluster API core API the module imports
//     (e.g. sigs.k8s.io/cluster-api/api/core/v1beta2 -> v1beta2);
//   - the served apiVersion(s) from the CRD itself.
//
// Bumping either side updates the label; nothing is duplicated.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

// coreImportRe matches an import of a Cluster API core API package. The
// captured group is the contract version, by CAPI's own convention that the
// contract version equals the core API version.
var coreImportRe = regexp.MustCompile(`sigs\.k8s\.io/cluster-api/api/core/(v[0-9a-z]+)`)

func main() {
	crdDir := "config/crd/bases"
	if len(os.Args) > 1 {
		crdDir = os.Args[1]
	}

	contracts, err := detectContracts([]string{"api", "internal", "cmd"})
	if err != nil {
		fail(err)
	}
	if len(contracts) == 0 {
		fail(fmt.Errorf("no sigs.k8s.io/cluster-api/api/core import found; cannot derive the contract version"))
	}

	crdFiles, err := filepath.Glob(filepath.Join(crdDir, "*.yaml"))
	if err != nil {
		fail(err)
	}
	for _, f := range crdFiles {
		if err := stamp(f, contracts); err != nil {
			fail(err)
		}
	}
}

// detectContracts walks the Go sources and returns the sorted set of Cluster API
// core API versions imported anywhere in the module.
func detectContracts(dirs []string) ([]string, error) {
	found := map[string]bool{}
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range coreImportRe.FindAllStringSubmatch(string(data), -1) {
				found[m[1]] = true
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	out := make([]string, 0, len(found))
	for v := range found {
		out = append(out, v)
	}
	sort.Strings(out)
	return out, nil
}

// stamp adds one contract label per contract version to a CRD file, with the
// CRD's served apiVersions as the value.
func stamp(path string, contracts []string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(data, &crd); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	served := make([]string, 0, len(crd.Spec.Versions))
	for _, v := range crd.Spec.Versions {
		if v.Served {
			served = append(served, v.Name)
		}
	}
	if len(served) == 0 {
		return nil
	}
	sort.Strings(served)
	value := strings.Join(served, "_")

	if crd.Labels == nil {
		crd.Labels = map[string]string{}
	}
	for _, c := range contracts {
		crd.Labels["cluster.x-k8s.io/"+c] = value
	}

	out, err := yaml.Marshal(&crd)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append([]byte("---\n"), out...), 0o644)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "contractlabels:", err)
	os.Exit(1)
}
