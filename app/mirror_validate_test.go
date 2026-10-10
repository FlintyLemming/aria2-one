package main

import (
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestNormalizeRepo(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"microsoft/PowerToys", "microsoft/PowerToys", true},
		{"  microsoft/PowerToys  ", "microsoft/PowerToys", true},
		{"owner/name.git", "owner/name", true},
		{"https://github.com/microsoft/PowerToys", "microsoft/PowerToys", true},
		{"https://github.com/microsoft/PowerToys/", "microsoft/PowerToys", true},
		{"https://github.com/microsoft/PowerToys.git", "microsoft/PowerToys", true},
		{"https://github.com/microsoft/PowerToys/releases", "microsoft/PowerToys", true},
		{"https://github.com/microsoft/PowerToys/releases/tag/v0.85.1", "microsoft/PowerToys", true},
		{"https://www.github.com/a/b?tab=readme#x", "a/b", true},
		{"http://github.com/a-b/c_d.e", "a-b/c_d.e", true},
		{"HTTPS://GitHub.com/A/B", "A/B", true},
		{"github.com/a/b/releases", "a/b", true},
		{"git@github.com:a/b.git", "a/b", true},
		{"", "", false},
		{"   ", "", false},
		{"justname", "", false},
		{"a/b/c", "", false},
		{"not a valid repo!", "", false},
		{"a b/c", "", false},
		{"https://gitlab.com/a/b", "", false},
		{"https://github.com/onlyowner", "", false},
		{"../b", "", false},
		{"a/..", "", false},
		{"a/.git", "", false},
	}
	for _, c := range cases {
		got, err := normalizeRepo(c.in)
		if c.ok {
			if err != nil || got != c.want {
				t.Errorf("normalizeRepo(%q) = %q, %v; want %q", c.in, got, err, c.want)
			}
			continue
		}
		var ve *validationError
		if err == nil || !errors.As(err, &ve) {
			t.Errorf("normalizeRepo(%q) = %q, %v; want validation error", c.in, got, err)
		}
	}
}

func TestNormalizeFolder(t *testing.T) {
	cases := []struct {
		folder, repo string
		want         string
		ok           bool
	}{
		{"PowerToys", "microsoft/PowerToys", "PowerToys", true},
		{"  Nezha Agent ", "nezhahq/agent", "Nezha Agent", true},
		{"", "microsoft/PowerToys", "PowerToys", true},
		{"   ", "a/b", "b", true},
		{"中文目录", "a/b", "中文目录", true},
		{"v1.0", "a/b", "v1.0", true},
		{".", "a/b", "", false},
		{"..", "a/b", "", false},
		{"a/b", "a/b", "", false},
		{`a\b`, "a/b", "", false},
		{"a\x00b", "a/b", "", false},
		{"tab\there", "a/b", "", false},
		{"line\nbreak", "a/b", "", false},
		{strings.Repeat("x", 128), "a/b", strings.Repeat("x", 128), true},
		{strings.Repeat("x", 129), "a/b", "", false},
		{strings.Repeat("中", 128), "a/b", strings.Repeat("中", 128), true},
	}
	for _, c := range cases {
		got, err := normalizeFolder(c.folder, c.repo)
		if c.ok != (err == nil) || got != c.want {
			t.Errorf("normalizeFolder(%q, %q) = %q, %v; want %q ok=%v", c.folder, c.repo, got, err, c.want, c.ok)
		}
	}
}

func TestNormalizeRootDir(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"github-mirror", "github-mirror", true},
		{"  github-mirror  ", "github-mirror", true},
		{"/github-mirror/", "github-mirror", true},
		{"mirrors/github", "mirrors/github", true},
		{"mirrors//github/", "mirrors/github", true},
		{"./mirrors", "mirrors", true},
		{"", "", false},
		{"/", "", false},
		{".", "", false},
		{"..", "", false},
		{"../x", "", false},
		{"a/../b", "", false},
		{"a/..", "", false},
		{`a\b`, "", false},
		{"a\x01b", "", false},
	}
	for _, c := range cases {
		got, err := normalizeRootDir(c.in)
		if c.ok != (err == nil) || got != c.want {
			t.Errorf("normalizeRootDir(%q) = %q, %v; want %q ok=%v", c.in, got, err, c.want, c.ok)
		}
	}
}

func TestValidateInterval(t *testing.T) {
	for _, n := range []int{0, 10, 360, 43200} {
		if err := validateInterval(n); err != nil {
			t.Errorf("validateInterval(%d) = %v, want nil", n, err)
		}
	}
	for _, n := range []int{-1, 1, 5, 9, 43201, 100000} {
		if err := validateInterval(n); err == nil {
			t.Errorf("validateInterval(%d) = nil, want error", n)
		}
	}
}

func TestNormalizeProxy(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"", "", true},
		{"   ", "", true},
		{"http://127.0.0.1:7890", "http://127.0.0.1:7890", true},
		{"http://127.0.0.1:7890/", "http://127.0.0.1:7890", true},
		{"http://proxy.lan", "http://proxy.lan", true},
		{"http://user:pass@proxy.lan:3128", "http://user:pass@proxy.lan:3128", true},
		{"http://[::1]:8080", "http://[::1]:8080", true},
		{"https://127.0.0.1:7890", "", false},
		{"socks5://127.0.0.1:1080", "", false},
		{"127.0.0.1:7890", "", false},
		{"http://", "", false},
		{"http://:8080", "", false},
		{"http://host:0", "", false},
		{"http://host:99999", "", false},
		{"http://host:abc", "", false},
		{"http://host:8080/path", "", false},
		{"http://host:8080?x=1", "", false},
		{"http://host:", "", false},
	}
	for _, c := range cases {
		got, err := normalizeProxy(c.in)
		if c.ok != (err == nil) || got != c.want {
			t.Errorf("normalizeProxy(%q) = %q, %v; want %q ok=%v", c.in, got, err, c.want, c.ok)
		}
	}
}

func TestNormalizeFileTypes(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{nil, []string{}},
		{[]string{}, []string{}},
		{[]string{"exe", "msi"}, []string{"exe", "msi"}},
		{[]string{" .exe ", "..msi", "", "  "}, []string{"exe", "msi"}},
		{[]string{"exe", "EXE", ".Exe"}, []string{"exe"}},
		{[]string{"tar.gz", ".tar.gz"}, []string{"tar.gz"}},
		{[]string{"exe, msi dmg", "zip；7z"}, []string{"exe", "msi", "dmg", "zip", "7z"}},
	}
	for _, c := range cases {
		got := normalizeFileTypes(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("normalizeFileTypes(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMatchAsset(t *testing.T) {
	mustRe := func(s string) *regexp.Regexp {
		re, err := compileMirrorRegex(s)
		if err != nil {
			t.Fatalf("compile %q: %v", s, err)
		}
		return re
	}
	cases := []struct {
		name      string
		fileTypes []string
		regex     string
		want      bool
	}{
		// 两者都空：全部下载
		{"anything.bin", nil, "", true},
		// 后缀：大小写不敏感，须以 .ext 结尾
		{"Setup.EXE", []string{"exe"}, "", true},
		{"setup.exe", []string{"EXE"}, "", true},
		{"setup.msi", []string{"exe", "msi"}, "", true},
		{"setup.exe.sha256", []string{"exe"}, "", false},
		{"notexe", []string{"exe"}, "", false},
		{"app-linux.tar.gz", []string{"tar.gz"}, "", true},
		{"app-linux.tgz", []string{"tar.gz"}, "", false},
		{"app-linux.tar.gz", []string{"gz"}, "", true},
		// 正则：部分匹配
		{"opentrace_osx_x64.zip", nil, "(osx|win)", true},
		{"opentrace_linux.zip", nil, "(osx|win)", false},
		{"app-v1-windows.zip", nil, "windows", true},
		// 导入后的锚定正则只从开头匹配
		{"osx_x64.zip", nil, "^(?:(osx|win))", true},
		{"opentrace_osx_x64.zip", nil, "^(?:(osx|win))", false},
		// 两者都设置：须同时满足
		{"app-windows.zip", []string{"zip"}, "windows", true},
		{"app-windows.exe", []string{"zip"}, "windows", false},
		{"app-linux.zip", []string{"zip"}, "windows", false},
	}
	for _, c := range cases {
		got := matchAsset(c.name, c.fileTypes, mustRe(c.regex))
		if got != c.want {
			t.Errorf("matchAsset(%q, %q, %q) = %v, want %v", c.name, c.fileTypes, c.regex, got, c.want)
		}
	}
}

func TestCompileMirrorRegexInvalid(t *testing.T) {
	for _, r := range []string{"(", "[a-", "(?=lookahead)", `\1`} {
		_, err := compileMirrorRegex(r)
		var ve *validationError
		if err == nil || !errors.As(err, &ve) {
			t.Errorf("compileMirrorRegex(%q) = %v, want validation error", r, err)
		}
	}
	if re, err := compileMirrorRegex(""); re != nil || err != nil {
		t.Errorf("empty regex should compile to nil, got %v %v", re, err)
	}
}

func TestSanitizePathName(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"v1.0.0", "v1.0.0", true},
		{"release/1.0", "release_1.0", true},
		{`win\x64`, "win_x64", true},
		{"../etc", ".._etc", true},
		{"/", "_", true},
		{"..", "", false},
		{".", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, err := sanitizePathName(c.in)
		if c.ok != (err == nil) || got != c.want {
			t.Errorf("sanitizePathName(%q) = %q, %v; want %q ok=%v", c.in, got, err, c.want, c.ok)
		}
	}
}

func TestNormalizeMirrorInput(t *testing.T) {
	m, err := normalizeMirrorInput(MirrorInput{Repo: "https://github.com/microsoft/PowerToys/releases"}, Mirror{Enabled: true, SaveChangelog: true})
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "PowerToys" || m.Folder != "PowerToys" || m.Repo != "microsoft/PowerToys" || !m.Enabled || !m.SaveChangelog {
		t.Errorf("defaults not applied: %+v", m)
	}
	if m.FileTypes == nil || len(m.FileTypes) != 0 {
		t.Errorf("fileTypes should be empty slice, got %#v", m.FileTypes)
	}

	m, err = normalizeMirrorInput(MirrorInput{
		Name: " My App ", Repo: "a/b", Folder: " Folder ", Enabled: boolPtr(false), SaveChangelog: boolPtr(false),
		FileTypes: flexStrings{".EXE", "exe", "msi"}, Regex: "win", IncludePrerelease: true, KeepVersions: 3,
	}, Mirror{Enabled: true, SaveChangelog: true})
	if err != nil {
		t.Fatal(err)
	}
	want := Mirror{Name: "My App", Repo: "a/b", Folder: "Folder", FileTypes: []string{"EXE", "msi"}, Regex: "win", IncludePrerelease: true, KeepVersions: 3}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("got %+v, want %+v", m, want)
	}

	bad := []MirrorInput{
		{Repo: ""},
		{Repo: "a/b", Regex: "("},
		{Repo: "a/b", Folder: ".."},
		{Repo: "a/b", KeepVersions: -1},
		{Repo: "a/b", Name: "bad\x00name"},
	}
	for _, in := range bad {
		if _, err := normalizeMirrorInput(in, Mirror{}); err == nil {
			t.Errorf("normalizeMirrorInput(%+v) should fail", in)
		}
	}
}

func TestFlexJSONTypes(t *testing.T) {
	var in MirrorInput
	if err := json.Unmarshal([]byte(`{"repo":"a/b","fileTypes":"exe, msi","keepVersions":"2"}`), &in); err != nil {
		t.Fatal(err)
	}
	if in.KeepVersions != 2 || !reflect.DeepEqual(normalizeFileTypes(in.FileTypes), []string{"exe", "msi"}) {
		t.Errorf("flex decode failed: %+v", in)
	}
	in = MirrorInput{}
	if err := json.Unmarshal([]byte(`{"fileTypes":null,"keepVersions":null,"enabled":null}`), &in); err != nil {
		t.Fatal(err)
	}
	if in.FileTypes != nil || in.KeepVersions != 0 || in.Enabled != nil {
		t.Errorf("null handling failed: %+v", in)
	}
	for _, body := range []string{`{"keepVersions":"abc"}`, `{"keepVersions":1.5}`, `{"fileTypes":{"a":1}}`, `{"fileTypes":[1,2]}`} {
		var x MirrorInput
		if err := json.Unmarshal([]byte(body), &x); err == nil {
			t.Errorf("expected error for %s", body)
		}
	}
	var n flexInt
	for body, want := range map[string]flexInt{`360`: 360, `"720"`: 720, `""`: 0, `" 15 "`: 15, `10.0`: 10} {
		if err := json.Unmarshal([]byte(body), &n); err != nil || n != want {
			t.Errorf("flexInt %s = %d, %v; want %d", body, n, err, want)
		}
	}
}

func TestFolderTaken(t *testing.T) {
	mirrors := []Mirror{{ID: "1", Folder: "PowerToys"}, {ID: "2", Folder: "Other"}}
	if !folderTaken(mirrors, "powertoys", "") {
		t.Error("case-insensitive duplicate not detected")
	}
	if folderTaken(mirrors, "POWERTOYS", "1") {
		t.Error("own folder should not count as taken")
	}
	if folderTaken(mirrors, "New", "") {
		t.Error("new folder reported as taken")
	}
}
