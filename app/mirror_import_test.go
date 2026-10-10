package main

import (
	"reflect"
	"strings"
	"testing"
)

// sampleScriptConfig 为原 github-release-mirror 脚本附带的 config.yaml
const sampleScriptConfig = `---
## 权限修改
# 是否以 root 运行。如果是，并且 fix-chown 为 true，则会根据 apply-uid 和 apply-gid修改所有产生的文件的所有者和组；如果不是，则不进行任何文件权限相关的修改。
run-as-root: false
fix-chown: false
apply-uid: 1000
apply-gid: 100
## 代理设置
proxy:
  enabled: false # 是否启用代理
  http: "http://127.0.0.1:7890" # HTTP 代理地址
  https: "http://127.0.0.1:7890" # HTTPS 代理地址
## 文件相关配置
saved-path: "/home/FlintyLemming/appdata/github-mirror"
## 跟踪配置
# 标题
track-list:
  # netbird:
  #   # （必填）Repo 的作者和项目名称，不需要完整地址
  #   repo-info: "netbirdio/netbird"
  #   # （选填）保存文件夹的名称，创建在 saved-path 下，如果不设置，使用该节标题名
  #   folder-name: "Netbird"
  #   # （选填）设置是否导出 What's Changed 为 md 文件，默认为 true
  #   get-changes-info: true
  #   # （选填）设置指定的后缀，只下载 Release 中指定后缀的文件
  #   file-type:
  #     - msi
  #     - deb
  #     - pkg
  opentrace:
    repo-info: "Archeb/opentrace"
    folder-name: "OpenTrace"
    get-changes-info: true
    regex: "(osx|win)"
  powertoys:
    repo-info: "microsoft/PowerToys"
    folder-name: "PowerToys"
    get-changes-info: true
  # rustdesk:
  #   repo-info: "rustdesk/rustdesk"
  #   folder-name: "RustDesk"
  #   file-type:
  #     - exe
  #     - dmg
  smartporxy:
    repo-info: "salarcode/SmartProxy"
    folder-name: "SmartProxy"
  ddns-go:
    repo-info: "jeessy2/ddns-go"
    folder-name: "ddns-go"
  sms-forwarder:
    repo-info: "pppscn/SmsForwarder"
    folder-name: "SmsForwarder"
  nezha-agent:
    repo-info: "nezhahq/agent"
    folder-name: "Nezha Agent"
  # massCode:
  #   repo-info: "massCodeIO/massCode"
  #   folder-name: "massCode"
  flclash:
    repo-info: "chen08209/FlClash"
    folder-name: "FlClash"
  immich:
    repo-info: "immich-app/immich"
    folder-name: "immich"
    file-type:
      - apk
  surge:
    repo-info: "LanYunDev/InjectLib_bak"
    folder-name: "Qurge"
  beszel:
    repo-info: "henrygd/beszel"
    folder-name: "Beszel"
  easytier:
    repo-info: "EasyTier/EasyTier"
    folder-name: "EasyTier"
`

func TestParseSampleScriptConfig(t *testing.T) {
	entries, err := parseMirrorYAML(sampleScriptConfig)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, e := range entries {
		if e.err != nil {
			t.Errorf("entry %s failed: %v", e.key, e.err)
		}
		keys = append(keys, e.key)
	}
	wantKeys := []string{"opentrace", "powertoys", "smartporxy", "ddns-go", "sms-forwarder", "nezha-agent", "flclash", "immich", "surge", "beszel", "easytier"}
	if !reflect.DeepEqual(keys, wantKeys) {
		t.Errorf("keys = %v\nwant %v (commented entries must be ignored, order preserved)", keys, wantKeys)
	}
	if got := entries[0].input.Regex; got != "^(?:(osx|win))" {
		t.Errorf("regex not anchored: %q", got)
	}
	if got := entries[7].input.FileTypes; !reflect.DeepEqual([]string(got), []string{"apk"}) {
		t.Errorf("immich fileTypes = %v", got)
	}
}

func TestImportSampleScriptConfig(t *testing.T) {
	env := newTestEnv(t, nil)
	res, err := env.m.ImportYAML(sampleScriptConfig)
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 11 || len(res.Skipped) != 0 || len(res.Mirrors) != 11 {
		t.Fatalf("imported=%d skipped=%v mirrors=%d", res.Imported, res.Skipped, len(res.Mirrors))
	}
	byName := map[string]Mirror{}
	for _, v := range res.Mirrors {
		byName[v.Name] = v.Mirror
		if !v.Enabled || v.IncludePrerelease || v.KeepVersions != 0 || v.ID == "" || v.CreatedAt.IsZero() {
			t.Errorf("defaults not applied to %+v", v.Mirror)
		}
		if v.State == nil || v.State.Status != StatusIdle {
			t.Errorf("imported mirror should be idle: %+v", v.State)
		}
	}
	ot := byName["opentrace"]
	if ot.Repo != "Archeb/opentrace" || ot.Folder != "OpenTrace" || !ot.SaveChangelog || ot.Regex != "^(?:(osx|win))" {
		t.Errorf("opentrace = %+v", ot)
	}
	if sp := byName["smartporxy"]; !sp.SaveChangelog || sp.Folder != "SmartProxy" || sp.Regex != "" || len(sp.FileTypes) != 0 {
		t.Errorf("smartporxy = %+v", sp)
	}
	if na := byName["nezha-agent"]; na.Folder != "Nezha Agent" || na.Repo != "nezhahq/agent" {
		t.Errorf("nezha-agent = %+v", na)
	}
	if im := byName["immich"]; !reflect.DeepEqual(im.FileTypes, []string{"apk"}) {
		t.Errorf("immich = %+v", im)
	}
	// 导入不自动同步
	env.m.Wait()
	if n := env.gh.requestCount(); n != 0 {
		t.Errorf("import must not trigger syncs, got %d GitHub requests", n)
	}
	// 已持久化
	_, views := env.m.ListMirrors()
	if len(views) != 11 {
		t.Errorf("mirrors after import = %d", len(views))
	}

	// 再次导入：全部因文件夹重复被跳过
	res, err = env.m.ImportYAML(sampleScriptConfig)
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 0 || len(res.Skipped) != 11 {
		t.Fatalf("re-import imported=%d skipped=%d", res.Imported, len(res.Skipped))
	}
	if res.Skipped[1].Key != "powertoys" || !strings.Contains(res.Skipped[1].Reason, "PowerToys") {
		t.Errorf("skip entry = %+v", res.Skipped[1])
	}
}

func TestImportEdgeCases(t *testing.T) {
	env := newTestEnv(t, nil)
	env.addMirror(t, MirrorInput{Repo: "x/existing", Folder: "powertoys"})
	yamlText := `
saved-path: /ignored
track-list:
  powertoys:
    repo-info: "microsoft/PowerToys"
    folder-name: "PowerToys"
  no-repo:
    folder-name: "X"
  empty:
  bad-repo:
    repo-info: "not a repo"
  bad-regex:
    repo-info: "a/b"
    folder-name: "BadRegex"
    regex: "(?=lookahead)"
  bad-folder:
    repo-info: "a/c"
    folder-name: "../escape"
  no-folder:
    repo-info: "owner/RepoName.git"
    get-changes-info: no
    file-type: exe
  dup-in-file:
    repo-info: "other/thing"
    folder-name: "reponame"
  py-style:
    repo-info: "https://github.com/a/py/releases"
    folder-name: "Py"
    get-changes-info: false
    file-type: [".EXE", "msi"]
    regex: "app-.*"
`
	res, err := env.m.ImportYAML(yamlText)
	if err != nil {
		t.Fatal(err)
	}
	skipped := map[string]string{}
	for _, s := range res.Skipped {
		skipped[s.Key] = s.Reason
	}
	wantSkipped := map[string]string{
		"powertoys":   "已被其它镜像使用",
		"no-repo":     "缺少 repo-info",
		"empty":       "缺少 repo-info",
		"bad-repo":    "仓库格式",
		"bad-regex":   "正则表达式无效",
		"bad-folder":  "文件夹名称",
		"dup-in-file": "已被其它镜像使用",
	}
	for k, sub := range wantSkipped {
		if !strings.Contains(skipped[k], sub) {
			t.Errorf("skipped[%s] = %q, want containing %q", k, skipped[k], sub)
		}
	}
	if res.Imported != 2 || len(res.Skipped) != len(wantSkipped) {
		t.Fatalf("imported=%d skipped=%v", res.Imported, res.Skipped)
	}
	nf := res.Mirrors[0].Mirror
	if nf.Name != "no-folder" || nf.Repo != "owner/RepoName" || nf.Folder != "RepoName" || nf.SaveChangelog || !reflect.DeepEqual(nf.FileTypes, []string{"exe"}) {
		t.Errorf("no-folder = %+v", nf)
	}
	py := res.Mirrors[1].Mirror
	if py.Repo != "a/py" || py.SaveChangelog || !reflect.DeepEqual(py.FileTypes, []string{"EXE", "msi"}) || py.Regex != "^(?:app-.*)" {
		t.Errorf("py-style = %+v", py)
	}
}

func TestImportInvalidDocuments(t *testing.T) {
	env := newTestEnv(t, nil)
	cases := map[string]string{
		"empty":          "   ",
		"syntax":         "track-list: [unclosed",
		"no track-list":  "saved-path: /x\n",
		"list not map":   "track-list:\n  - a\n  - b\n",
		"scalar top":     "just a string",
		"track-list str": "track-list: hello",
	}
	for name, text := range cases {
		if _, err := env.m.ImportYAML(text); err == nil {
			t.Errorf("%s: expected error", name)
		} else if _, ok := err.(*validationError); !ok {
			t.Errorf("%s: expected validation error, got %T %v", name, err, err)
		}
	}
	// 全部被注释掉：track-list 为 null，导入 0 个
	res, err := env.m.ImportYAML("track-list:\n  # a:\n  #   repo-info: x/y\n")
	if err != nil || res.Imported != 0 || res.Skipped == nil || res.Mirrors == nil {
		t.Errorf("all-commented import = %+v, %v", res, err)
	}
}

func TestConvertPythonRegex(t *testing.T) {
	if convertPythonRegex("") != "" {
		t.Error("empty regex must stay empty")
	}
	if got := convertPythonRegex("a|b"); got != "^(?:a|b)" {
		t.Errorf("got %q", got)
	}
	re, err := compileMirrorRegex(convertPythonRegex("a|b"))
	if err != nil {
		t.Fatal(err)
	}
	// Python re.match("a|b", "xb") 不匹配；直接部分匹配 "a|b" 则会误匹配
	if re.MatchString("xb") || !re.MatchString("bx") {
		t.Error("alternation must be anchored as a whole")
	}
}
