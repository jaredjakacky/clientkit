package main

import "testing"

func TestValidateRelease(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		modulePath string
		version    string
		wantErr    bool
	}{
		{name: "v0", modulePath: "example.com/project", version: "v0.4.0"},
		{name: "v1", modulePath: "example.com/project", version: "v1.2.3"},
		{name: "prerelease", modulePath: "example.com/project", version: "v1.2.3-rc.1"},
		{name: "v2 suffix", modulePath: "example.com/project/v2", version: "v2.0.0"},
		{name: "v2 suffix prerelease", modulePath: "example.com/project/v2", version: "v2.0.0-beta.2"},
		{name: "v2 without suffix", modulePath: "example.com/project", version: "v2.0.0", wantErr: true},
		{name: "v1 with v2 suffix", modulePath: "example.com/project/v2", version: "v1.2.3", wantErr: true},
		{name: "v3 with v2 suffix", modulePath: "example.com/project/v2", version: "v3.0.0", wantErr: true},
		{name: "v1 path suffix", modulePath: "example.com/project/v1", version: "v1.2.3", wantErr: true},
		{name: "abbreviated major", modulePath: "example.com/project", version: "v1", wantErr: true},
		{name: "abbreviated minor", modulePath: "example.com/project", version: "v1.2", wantErr: true},
		{name: "leading zero", modulePath: "example.com/project", version: "v01.2.3", wantErr: true},
		{name: "prerelease leading zero", modulePath: "example.com/project", version: "v1.2.3-01", wantErr: true},
		{name: "build metadata", modulePath: "example.com/project", version: "v1.2.3+build.1", wantErr: true},
		{name: "incompatible suffix", modulePath: "example.com/project", version: "v1.2.3+incompatible", wantErr: true},
		{name: "malformed module path", modulePath: "example.com//project", version: "v1.2.3", wantErr: true},
		{name: "empty module path", version: "v1.2.3", wantErr: true},
		{name: "empty version", modulePath: "example.com/project", wantErr: true},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := validateRelease(test.modulePath, test.version)
			if test.wantErr && err == nil {
				t.Fatal("validateRelease() error = nil, want an error")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("validateRelease() error = %v, want nil", err)
			}
		})
	}
}

func TestRunRequiresModulePathAndVersion(t *testing.T) {
	t.Parallel()

	if err := run([]string{"example.com/project"}); err == nil {
		t.Fatal("run() error = nil, want an error")
	}
}
