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

package pki

import (
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
)

func TestGenerateOperatorCA(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "an operator CA and client the management API admits"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ca, err := GenerateOperatorCA("demo")
			if err != nil {
				t.Fatalf("GenerateOperatorCA() error = %v", err)
			}

			caCert := parsePEMCert(t, ca.Cert)
			if !caCert.IsCA {
				t.Error("the CA certificate is not a CA")
			}
			clientCert := parsePEMCert(t, ca.ClientCert)

			// The client must be signed by the CA, or the node would reject it.
			if err := clientCert.CheckSignatureFrom(caCert); err != nil {
				t.Errorf("the client certificate is not signed by the CA: %v", err)
			}

			// The identity the API's authorize() accepts.
			var admitted bool
			for _, org := range clientCert.Subject.Organization {
				if org == "vates:admin" {
					admitted = true
				}
			}
			if !admitted {
				t.Errorf("client organizations = %v, want one to be vates:admin", clientCert.Subject.Organization)
			}
			if !strings.Contains(clientCert.Subject.CommonName, "demo") {
				t.Errorf("client common name = %q, want it to name the cluster", clientCert.Subject.CommonName)
			}
		})
	}
}

func TestSecretDataRoundTrip(t *testing.T) {
	ca, err := GenerateOperatorCA("demo")
	if err != nil {
		t.Fatalf("GenerateOperatorCA() error = %v", err)
	}

	back, err := FromSecretData(ca.SecretData())
	if err != nil {
		t.Fatalf("FromSecretData() error = %v", err)
	}
	if string(back.Cert) != string(ca.Cert) || string(back.ClientKey) != string(ca.ClientKey) {
		t.Error("the operator CA did not round-trip through Secret data")
	}
}

func parsePEMCert(t *testing.T, raw []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatal("not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parsing certificate: %v", err)
	}
	return cert
}
