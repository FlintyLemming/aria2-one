package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// validationError 表示用户输入校验失败（API 返回 400）
type validationError struct {
	msg string
}

func (e *validationError) Error() string { return e.msg }

func invalidf(format string, a ...interface{}) error {
	return &validationError{msg: fmt.Sprintf(format, a...)}
}

func hasControlChar(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// normalizeRepo 接受 owner/name、GitHub URL（可带 .git 或后续路径）、git@github.com:owner/name.git，统一为 owner/name
func normalizeRepo(input string) (string, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return "", invalidf("仓库不能为空")
	}
	lower := strings.ToLower(s)
	isURL := false
	switch {
	case strings.HasPrefix(lower, "git@github.com:"):
		s = s[len("git@github.com:"):]
		isURL = true
	case strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://"):
		u, err := url.Parse(s)
		if err != nil {
			return "", invalidf("仓库地址无效：%s", input)
		}
		host := strings.ToLower(u.Hostname())
		if host != "github.com" && host != "www.github.com" {
			return "", invalidf("仅支持 github.com 上的仓库：%s", input)
		}
		s = u.Path
		isURL = true
	case strings.HasPrefix(lower, "github.com/") || strings.HasPrefix(lower, "www.github.com/"):
		s = s[strings.Index(s, "/")+1:]
		isURL = true
	}
	s = strings.Trim(s, "/")
	parts := strings.Split(s, "/")
	if len(parts) < 2 || (!isURL && len(parts) != 2) {
		return "", invalidf("仓库格式应为 owner/name：%s", input)
	}
	owner := parts[0]
	name := strings.TrimSuffix(parts[1], ".git")
	repo := owner + "/" + name
	if !repoPattern.MatchString(repo) || owner == "." || owner == ".." || name == "." || name == ".." {
		return "", invalidf("仓库格式应为 owner/name：%s", input)
	}
	return repo, nil
}

// repoShortName 返回 owner/name 中的 name 部分
func repoShortName(repo string) string {
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		return repo[i+1:]
	}
	return repo
}

// normalizeFolder 校验镜像文件夹名（单级目录名）
func normalizeFolder(folder, repo string) (string, error) {
	f := strings.TrimSpace(folder)
	if f == "" {
		f = repoShortName(repo)
	}
	if !utf8.ValidString(f) {
		return "", invalidf("文件夹名称包含非法字符")
	}
	if f == "." || f == ".." {
		return "", invalidf("文件夹名称不能为 . 或 ..")
	}
	if strings.ContainsAny(f, `/\`) {
		return "", invalidf("文件夹名称不能包含 / 或 \\")
	}
	if hasControlChar(f) {
		return "", invalidf("文件夹名称不能包含控制字符")
	}
	if utf8.RuneCountInString(f) > maxMirrorFolderLength {
		return "", invalidf("文件夹名称长度不能超过 %d 个字符", maxMirrorFolderLength)
	}
	return f, nil
}

func isListSeparator(r rune) bool {
	return r == ',' || r == '，' || r == '、' || r == ';' || r == '；' || unicode.IsSpace(r)
}

// normalizeFileTypes 按逗号/空格拆分、去空格、去前导 .、丢弃空项、大小写不敏感去重
func normalizeFileTypes(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, item := range in {
		for _, t := range strings.FieldsFunc(item, isListSeparator) {
			t = strings.TrimSpace(strings.TrimLeft(t, "."))
			if t == "" {
				continue
			}
			key := strings.ToLower(t)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, t)
		}
	}
	return out
}

// flexInt 接受 JSON 数字或数字字符串（表单输入框可能提交字符串），"" 视为 0
type flexInt int

func (n *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		return nil
	}
	if strings.HasPrefix(s, `"`) {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return invalidf("数值格式无效")
		}
		s = strings.TrimSpace(str)
		if s == "" {
			*n = 0
			return nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		f, ferr := strconv.ParseFloat(s, 64)
		if ferr != nil || f != math.Trunc(f) || math.Abs(f) > 1e9 {
			return invalidf("数值必须是整数：%s", s)
		}
		v = int(f)
	}
	*n = flexInt(v)
	return nil
}

// flexStrings 接受字符串数组，或以逗号/空格分隔的单个字符串
type flexStrings []string

func (l *flexStrings) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		*l = nil
		return nil
	}
	if strings.HasPrefix(s, `"`) {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return invalidf("文件后缀格式无效")
		}
		*l = flexStrings{str}
		return nil
	}
	var arr []string
	if err := json.Unmarshal(b, &arr); err != nil {
		return invalidf("文件后缀必须是字符串数组")
	}
	*l = arr
	return nil
}

// compileMirrorRegex 编译过滤正则（RE2，部分匹配），空串返回 nil
func compileMirrorRegex(expr string) (*regexp.Regexp, error) {
	if expr == "" {
		return nil, nil
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, invalidf("正则表达式无效：%v", err)
	}
	return re, nil
}

// matchAsset 判断资产是否命中过滤条件：fileTypes 非空须命中其一，regex 非空须匹配
func matchAsset(name string, fileTypes []string, re *regexp.Regexp) bool {
	if len(fileTypes) > 0 {
		lowerName := strings.ToLower(name)
		hit := false
		for _, ext := range fileTypes {
			if strings.HasSuffix(lowerName, "."+strings.ToLower(ext)) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	if re != nil && !re.MatchString(name) {
		return false
	}
	return true
}

// sanitizePathName 将 tag / 资产名转为安全的单级文件名
func sanitizePathName(s string) (string, error) {
	r := strings.NewReplacer("/", "_", `\`, "_").Replace(s)
	if r == "" || r == "." || r == ".." {
		return "", fmt.Errorf("非法的名称：%q", s)
	}
	return r, nil
}

// normalizeRootDir 规范化设置中的根目录（相对 downloadDir 的多级相对路径）
func normalizeRootDir(s string) (string, error) {
	r := strings.Trim(strings.TrimSpace(s), "/")
	if r == "" {
		return "", invalidf("根目录不能为空")
	}
	if strings.Contains(r, `\`) {
		return "", invalidf("根目录不能包含反斜杠")
	}
	if hasControlChar(r) || !utf8.ValidString(r) {
		return "", invalidf("根目录包含非法字符")
	}
	for _, seg := range strings.Split(r, "/") {
		if seg == ".." {
			return "", invalidf("根目录不能包含 ..")
		}
	}
	c := path.Clean(r)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") || strings.HasPrefix(c, "/") {
		return "", invalidf("根目录必须是 downloads 下的相对路径")
	}
	return c, nil
}

// validateInterval 检查间隔：0（仅手动）或 10~43200 分钟
func validateInterval(n int) error {
	if n == 0 || (n >= minMirrorInterval && n <= maxMirrorInterval) {
		return nil
	}
	return invalidf("检查间隔必须为 0（仅手动）或 %d~%d 分钟", minMirrorInterval, maxMirrorInterval)
}

// normalizeProxy 仅允许 http://[user:pass@]host[:port]（aria2 all-proxy 只支持 HTTP 代理）
func normalizeProxy(s string) (string, error) {
	p := strings.TrimSpace(s)
	if p == "" {
		return "", nil
	}
	bad := invalidf("代理地址格式应为 http://[user:pass@]host[:port]")
	u, err := url.Parse(p)
	if err != nil || u.Scheme != "http" || u.Opaque != "" || u.Hostname() == "" {
		return "", bad
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return "", bad
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", bad
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return "", bad
	}
	return (&url.URL{Scheme: "http", User: u.User, Host: u.Host}).String(), nil
}

// MirrorInput 为 POST/PUT /api/mirrors 的请求体
type MirrorInput struct {
	Name              string      `json:"name"`
	Repo              string      `json:"repo"`
	Folder            string      `json:"folder"`
	Enabled           *bool       `json:"enabled"`
	SaveChangelog     *bool       `json:"saveChangelog"`
	FileTypes         flexStrings `json:"fileTypes"`
	Regex             string      `json:"regex"`
	IncludePrerelease bool        `json:"includePrerelease"`
	KeepVersions      flexInt     `json:"keepVersions"`
}

// normalizeMirrorInput 校验并规范化输入；enabled / saveChangelog 缺省时使用 defaults 中的值
func normalizeMirrorInput(in MirrorInput, defaults Mirror) (Mirror, error) {
	repo, err := normalizeRepo(in.Repo)
	if err != nil {
		return Mirror{}, err
	}
	folder, err := normalizeFolder(in.Folder, repo)
	if err != nil {
		return Mirror{}, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = repoShortName(repo)
	}
	if hasControlChar(name) || !utf8.ValidString(name) {
		return Mirror{}, invalidf("名称包含非法字符")
	}
	regex := strings.TrimSpace(in.Regex)
	if _, err := compileMirrorRegex(regex); err != nil {
		return Mirror{}, err
	}
	if in.KeepVersions < 0 {
		return Mirror{}, invalidf("保留版本数不能小于 0")
	}
	m := Mirror{
		Name:              name,
		Repo:              repo,
		Folder:            folder,
		Enabled:           defaults.Enabled,
		SaveChangelog:     defaults.SaveChangelog,
		FileTypes:         normalizeFileTypes(in.FileTypes),
		Regex:             regex,
		IncludePrerelease: in.IncludePrerelease,
		KeepVersions:      int(in.KeepVersions),
	}
	if in.Enabled != nil {
		m.Enabled = *in.Enabled
	}
	if in.SaveChangelog != nil {
		m.SaveChangelog = *in.SaveChangelog
	}
	return m, nil
}

// folderTaken 判断文件夹名（大小写不敏感）是否已被其它镜像使用
func folderTaken(mirrors []Mirror, folder, exceptID string) bool {
	for _, m := range mirrors {
		if m.ID != exceptID && strings.EqualFold(m.Folder, folder) {
			return true
		}
	}
	return false
}
