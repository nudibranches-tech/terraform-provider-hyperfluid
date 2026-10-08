// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"reflect"
	"strings"
	"testing"
)

func TestBuildSecretValueScmCredential(t *testing.T) {
	for name, tc := range map[string]struct {
		in   string
		want map[string]interface{}
	}{
		"token only": {
			`{"provider":"github","token":"ghp_x"}`,
			map[string]interface{}{"provider": "github", "token": "ghp_x"},
		},
		"every field": {
			`{"provider":"gitlab","username":"deploy","base_url":"https://gitlab.example.com","token":"glpat-x"}`,
			map[string]interface{}{"provider": "gitlab", "username": "deploy", "base_url": "https://gitlab.example.com", "token": "glpat-x"},
		},
		"blank optional fields are left out, a trailing slash is trimmed": {
			`{"provider":"forgejo","username":"  ","base_url":"https://forge.example.com/","token":"t"}`,
			map[string]interface{}{"provider": "forgejo", "base_url": "https://forge.example.com", "token": "t"},
		},
		"null optional fields": {
			`{"provider":"github","username":null,"base_url":null,"token":"t"}`,
			map[string]interface{}{"provider": "github", "token": "t"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := BuildSecretValue("scm_credential", tc.in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// The platform validates the bare credential, never an envelope.
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("value = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestBuildSecretValueScmCredentialRejects(t *testing.T) {
	const token = "ghp_super-secret-token"
	for name, in := range map[string]string{
		"not an object":        `"` + token + `"`,
		"not JSON":             token,
		"no provider":          `{"token":"` + token + `"}`,
		"unknown provider":     `{"provider":"bitbucket","token":"` + token + `"}`,
		"no token":             `{"provider":"github"}`,
		"blank token":          `{"provider":"github","token":"  "}`,
		"unknown key":          `{"provider":"github","token":"` + token + `","tokn":"x"}`,
		"a non-string field":   `{"provider":"github","token":"` + token + `","username":7}`,
		"a non-string token":   `{"provider":"github","token":["` + token + `"]}`,
		"a non-string baseurl": `{"provider":"github","token":"` + token + `","base_url":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := BuildSecretValue("scm_credential", in)
			if err == nil {
				t.Fatal("want an error")
			}
			if strings.Contains(err.Error(), token) {
				t.Errorf("the error quotes the token: %v", err)
			}
		})
	}
}

func TestBuildSecretValueUnknownType(t *testing.T) {
	if _, err := BuildSecretValue("oci_registry_config", "{}"); err == nil {
		t.Fatal("an unsupported type must be refused")
	}
}
