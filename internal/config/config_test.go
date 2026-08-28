package config

import (
	"strings"
	"testing"
)

const descriptorHeader = "[scaffold]\nversion = 1\n"

func TestParseDescriptorValid(t *testing.T) {
	d, err := ParseDescriptor([]byte(`
[scaffold]
version = 1

[template]
name = "go-service"

[[arguments]]
name = "project_name"
description = "Project name"
token = "@@PROJECT_NAME@@"

[[arguments]]
name = "module"

[[arguments]]
name = "go_version"
default = "1.26"
token = "@@GO_VERSION@@"

[[files]]
path = "Makefile"

[[files]]
path = "go.mod"

[files.arguments.module]
token = "@@MODULE@@"

[[files]]
path = ".golangci.yml"
patch = "json-patch"

[[files]]
path = "examples/**/*"
substitute = false
allow-empty = true

[[files]]
path = "docs/**/*.txt"

[files.arguments.project_name]
enabled = false
`))
	if err != nil {
		t.Fatalf("ParseDescriptor: %v", err)
	}
	if d.TemplateName != "go-service" {
		t.Errorf("TemplateName = %q", d.TemplateName)
	}
	if len(d.Arguments) != 3 {
		t.Fatalf("len(Arguments) = %d", len(d.Arguments))
	}
	if a := d.Argument("project_name"); a == nil || a.Token != "@@PROJECT_NAME@@" || a.Default != nil {
		t.Errorf("project_name = %+v", a)
	}
	if a := d.Argument("module"); a == nil || a.Token != "" || a.Default != nil {
		t.Errorf("module = %+v", a)
	}
	if a := d.Argument("go_version"); a == nil || a.Default == nil || *a.Default != "1.26" {
		t.Errorf("go_version = %+v", a)
	}
	if len(d.Files) != 5 {
		t.Fatalf("len(Files) = %d", len(d.Files))
	}
	if f := d.Files[0]; !f.Substitute || f.AllowEmpty || f.Patch != "" {
		t.Errorf("Makefile rule = %+v", f)
	}
	if f := d.Files[1]; f.Arguments["module"].Token == nil || *f.Arguments["module"].Token != "@@MODULE@@" {
		t.Errorf("go.mod rule = %+v", f)
	}
	if f := d.Files[2]; f.Patch != "json-patch" {
		t.Errorf("golangci rule = %+v", f)
	}
	if f := d.Files[3]; f.Substitute || !f.AllowEmpty {
		t.Errorf("examples rule = %+v", f)
	}
	if r := d.Files[4].Arguments["project_name"]; r.Enabled || r.Token != nil {
		t.Errorf("docs project_name rule = %+v", r)
	}
}

func TestParseDescriptorErrors(t *testing.T) {
	tests := []struct {
		name    string
		toml    string
		wantErr string
	}{
		{"invalid TOML", "[scaffold\nversion = 1\n", "invalid TOML"},
		{"missing version", "[template]\nname = \"x\"\n", "missing required [scaffold] version"},
		{"unsupported version", "[scaffold]\nversion = 2\n", "unsupported scaffold version 2"},
		{
			"duplicate argument names",
			descriptorHeader + "[[arguments]]\nname = \"a\"\n[[arguments]]\nname = \"a\"\n",
			`duplicate argument name "a"`,
		},
		{
			"missing argument name",
			descriptorHeader + "[[arguments]]\ndescription = \"x\"\n",
			"arguments[0]: missing required name",
		},
		{
			"empty global token",
			descriptorHeader + "[[arguments]]\nname = \"a\"\ntoken = \"\"\n",
			"explicit token must not be empty",
		},
		{
			"empty per-rule token",
			descriptorHeader + "[[arguments]]\nname = \"a\"\n" +
				"[[files]]\npath = \"x\"\n[files.arguments.a]\ntoken = \"\"\n",
			"explicit token must not be empty",
		},
		{
			"undefined argument in file rule",
			descriptorHeader + "[[files]]\npath = \"x\"\n[files.arguments.nope]\ntoken = \"@@X@@\"\n",
			`references undefined argument "nope"`,
		},
		{
			"substitute false with per-rule argument config",
			descriptorHeader + "[[arguments]]\nname = \"a\"\n" +
				"[[files]]\npath = \"x\"\nsubstitute = false\n[files.arguments.a]\ntoken = \"@@A@@\"\n",
			"substitute = false cannot be combined",
		},
		{
			"missing file path",
			descriptorHeader + "[[files]]\npatch = \"json-patch\"\n",
			"files[0]: missing required path",
		},
		{
			"absolute file pattern",
			descriptorHeader + "[[files]]\npath = \"/etc/passwd\"\n",
			"must not be absolute",
		},
		{
			"repo-escaping file pattern",
			descriptorHeader + "[[files]]\npath = \"../../outside\"\n",
			"must not escape the repository",
		},
		{
			"repo-escaping glob",
			descriptorHeader + "[[files]]\npath = \"a/../../*.yml\"\n",
			"must not escape the repository",
		},
		{
			"unknown patch strategy",
			descriptorHeader + "[[files]]\npath = \"x\"\npatch = \"yaml-merge\"\n",
			`unsupported patch strategy "yaml-merge"`,
		},
		{
			// §48: paths are forward-slash repo-relative on every platform.
			"backslash file pattern",
			descriptorHeader + "[[files]]\npath = 'sub\\evil.txt'\n",
			"must use forward slashes",
		},
		{
			"windows drive-absolute file pattern",
			descriptorHeader + "[[files]]\npath = \"C:/evil\"\n",
			"must not carry a drive prefix",
		},
		{
			"windows drive-relative file pattern",
			descriptorHeader + "[[files]]\npath = \"c:evil\"\n",
			"must not carry a drive prefix",
		},
		{
			"UNC file pattern",
			descriptorHeader + "[[files]]\npath = '\\\\host\\share\\evil'\n",
			"must use forward slashes",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseDescriptor([]byte(tt.toml))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseTargetValid(t *testing.T) {
	c, err := ParseTarget([]byte(`
[scaffold]
version = 1

[source]
git = "https://github.com/acme/go-template.git"
ref = "main"

[args]
project_name = "orders"
module = "github.com/acme/orders"

[overrides.".golangci.yml"]
strategy = "json-patch"
patches = [
    "patches/golangci.json"
]

[propose]
create-command = ["forge", "pr", "create", "--head", "{{ branch }}"]
`))
	if err != nil {
		t.Fatalf("ParseTarget: %v", err)
	}
	if c.SourceGit != "https://github.com/acme/go-template.git" || c.SourceRef != "main" {
		t.Errorf("source = %q %q", c.SourceGit, c.SourceRef)
	}
	if c.Args["project_name"] != "orders" || c.Args["module"] != "github.com/acme/orders" {
		t.Errorf("args = %v", c.Args)
	}
	o, ok := c.Overrides[".golangci.yml"]
	if !ok || o.Strategy != "json-patch" || len(o.Patches) != 1 || o.Patches[0] != "patches/golangci.json" {
		t.Errorf("override = %+v", o)
	}
	if len(c.ProposeCreateCommand) != 5 || c.ProposeCreateCommand[0] != "forge" {
		t.Errorf("create-command = %v", c.ProposeCreateCommand)
	}
}

func TestParseTargetOptionalRef(t *testing.T) {
	c, err := ParseTarget([]byte("[scaffold]\nversion = 1\n[source]\ngit = \"x.git\"\n"))
	if err != nil {
		t.Fatalf("ParseTarget: %v", err)
	}
	if c.SourceRef != "" {
		t.Errorf("SourceRef = %q", c.SourceRef)
	}
}

func TestParseTargetErrors(t *testing.T) {
	header := "[scaffold]\nversion = 1\n[source]\ngit = \"x.git\"\n"
	tests := []struct {
		name    string
		toml    string
		wantErr string
	}{
		{"invalid TOML", "[scaffold]]\n", "invalid TOML"},
		{"missing version", "[source]\ngit = \"x.git\"\n", "missing required [scaffold] version"},
		{"unsupported version", "[scaffold]\nversion = 99\n[source]\ngit = \"x.git\"\n", "unsupported scaffold version 99"},
		{"missing source", "[scaffold]\nversion = 1\n", "missing required [source] git"},
		{"missing source git", "[scaffold]\nversion = 1\n[source]\nref = \"main\"\n", "missing required [source] git"},
		{
			// §24: override keys must be concrete paths.
			"glob override key",
			header + "[overrides.\"config/*.yml\"]\nstrategy = \"json-patch\"\npatches = [\"patches/x.json\"]\n",
			"must be concrete paths, not globs",
		},
		{
			"absolute override key",
			header + "[overrides.\"/etc/passwd\"]\nstrategy = \"json-patch\"\npatches = []\n",
			"must not be absolute",
		},
		{
			"missing override strategy",
			header + "[overrides.\"a.yml\"]\npatches = [\"patches/x.json\"]\n",
			"missing patch strategy",
		},
		{
			"unknown override strategy",
			header + "[overrides.\"a.yml\"]\nstrategy = \"three-way\"\npatches = []\n",
			`unsupported patch strategy "three-way"`,
		},
		{
			// §48: patch paths must remain beneath .git-scaffold/.
			"escaping patch path",
			header + "[overrides.\"a.yml\"]\nstrategy = \"json-patch\"\npatches = [\"../evil.json\"]\n",
			"must not escape the repository",
		},
		{
			"nested escaping patch path",
			header + "[overrides.\"a.yml\"]\nstrategy = \"json-patch\"\npatches = [\"patches/../../evil.json\"]\n",
			"must not escape the repository",
		},
		{
			"absolute patch path",
			header + "[overrides.\"a.yml\"]\nstrategy = \"json-patch\"\npatches = [\"/etc/evil.json\"]\n",
			"must not be absolute",
		},
		{
			// §48: forward slashes only, on every platform.
			"backslash override key",
			header + "[overrides.'sub\\evil.yml']\nstrategy = \"json-patch\"\npatches = []\n",
			"must use forward slashes",
		},
		{
			"backslash patch path",
			header + "[overrides.\"a.yml\"]\nstrategy = \"json-patch\"\npatches = ['patches\\evil.json']\n",
			"must use forward slashes",
		},
		{
			"drive-absolute patch path",
			header + "[overrides.\"a.yml\"]\nstrategy = \"json-patch\"\npatches = [\"C:/evil.json\"]\n",
			"must not carry a drive prefix",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseTarget([]byte(tt.toml))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}
