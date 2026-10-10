package main

import (
	"fmt"
	"log"
	"strings"

	"gopkg.in/yaml.v3"
)

// ImportSkip 导入时被跳过的条目及原因
type ImportSkip struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

// ImportResult 为 POST /api/mirrors/import 的响应；mirrors 为本次新导入的镜像
type ImportResult struct {
	Imported int          `json:"imported"`
	Skipped  []ImportSkip `json:"skipped"`
	Mirrors  []MirrorView `json:"mirrors"`
}

// yamlBool 兼容 YAML 1.1 风格的 yes/no/on/off（原脚本使用 PyYAML）
type yamlBool struct {
	set   bool
	value bool
}

func (b *yamlBool) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("应为布尔值")
	}
	switch strings.ToLower(strings.TrimSpace(n.Value)) {
	case "true", "yes", "y", "on", "1":
		b.set, b.value = true, true
	case "false", "no", "n", "off", "0":
		b.set, b.value = true, false
	case "", "~", "null":
	default:
		return fmt.Errorf("应为布尔值：%s", n.Value)
	}
	return nil
}

// yamlStrings 兼容单个字符串或字符串列表
type yamlStrings []string

func (l *yamlStrings) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Tag == "!!null" {
			*l = nil
			return nil
		}
		*l = yamlStrings{n.Value}
		return nil
	case yaml.SequenceNode:
		out := yamlStrings{}
		for _, item := range n.Content {
			if item.Kind != yaml.ScalarNode {
				return fmt.Errorf("file-type 应为字符串列表")
			}
			if item.Tag == "!!null" {
				continue
			}
			out = append(out, item.Value)
		}
		*l = out
		return nil
	default:
		return fmt.Errorf("file-type 应为字符串列表")
	}
}

type yamlTrackItem struct {
	RepoInfo       string      `yaml:"repo-info"`
	FolderName     string      `yaml:"folder-name"`
	GetChangesInfo yamlBool    `yaml:"get-changes-info"`
	FileType       yamlStrings `yaml:"file-type"`
	Regex          string      `yaml:"regex"`
}

type importEntry struct {
	key   string
	input MirrorInput
	err   error
}

// convertPythonRegex 原脚本使用 Python re.match（从开头锚定），转换为 ^(?:原值) 以保持语义
func convertPythonRegex(r string) string {
	if r == "" {
		return ""
	}
	return "^(?:" + r + ")"
}

// parseMirrorYAML 只读取 track-list 映射，按原顺序返回条目；被注释掉的条目 YAML 本身不会解析出来
func parseMirrorYAML(text string) ([]importEntry, error) {
	if strings.TrimSpace(text) == "" {
		return nil, invalidf("YAML 内容为空")
	}
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(text), &root); err != nil {
		return nil, invalidf("YAML 解析失败：%v", err)
	}
	doc := &root
	if doc.Kind == yaml.DocumentNode {
		if len(doc.Content) == 0 {
			return nil, invalidf("YAML 内容为空")
		}
		doc = doc.Content[0]
	}
	if doc.Kind != yaml.MappingNode {
		return nil, invalidf("YAML 顶层应为映射")
	}
	var list *yaml.Node
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value == "track-list" {
			list = doc.Content[i+1]
		}
	}
	if list == nil {
		return nil, invalidf("YAML 中没有 track-list")
	}
	if list.Kind == yaml.AliasNode && list.Alias != nil {
		list = list.Alias
	}
	if list.Kind == yaml.ScalarNode && list.Tag == "!!null" {
		return []importEntry{}, nil
	}
	if list.Kind != yaml.MappingNode {
		return nil, invalidf("track-list 应为映射")
	}

	entries := []importEntry{}
	for i := 0; i+1 < len(list.Content); i += 2 {
		key := list.Content[i].Value
		node := list.Content[i+1]
		e := importEntry{key: key}
		var item yamlTrackItem
		if node.Kind == yaml.ScalarNode && node.Tag == "!!null" {
			e.err = invalidf("缺少 repo-info")
		} else if err := node.Decode(&item); err != nil {
			e.err = invalidf("格式错误：%v", err)
		} else if strings.TrimSpace(item.RepoInfo) == "" {
			e.err = invalidf("缺少 repo-info")
		}
		if e.err == nil {
			enabled := true
			saveChangelog := true
			if item.GetChangesInfo.set {
				saveChangelog = item.GetChangesInfo.value
			}
			e.input = MirrorInput{
				Name:          key,
				Repo:          item.RepoInfo,
				Folder:        item.FolderName,
				Enabled:       &enabled,
				SaveChangelog: &saveChangelog,
				FileTypes:     flexStrings(item.FileType),
				Regex:         convertPythonRegex(item.Regex),
			}
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// ImportYAML 导入原脚本的 config.yaml；失败或文件夹重复的条目进入 skipped，导入后不自动同步
func (m *Manager) ImportYAML(text string) (*ImportResult, error) {
	entries, err := parseMirrorYAML(text)
	if err != nil {
		return nil, err
	}
	res := &ImportResult{Skipped: []ImportSkip{}, Mirrors: []MirrorView{}}
	err = m.store.update(func(d *mirrorData) error {
		res.Imported = 0
		res.Skipped = res.Skipped[:0]
		res.Mirrors = res.Mirrors[:0]
		for _, e := range entries {
			if e.err != nil {
				res.Skipped = append(res.Skipped, ImportSkip{Key: e.key, Reason: e.err.Error()})
				continue
			}
			mi, err := normalizeMirrorInput(e.input, Mirror{Enabled: true, SaveChangelog: true})
			if err != nil {
				res.Skipped = append(res.Skipped, ImportSkip{Key: e.key, Reason: err.Error()})
				continue
			}
			if folderTaken(d.Mirrors, mi.Folder, "") {
				res.Skipped = append(res.Skipped, ImportSkip{Key: e.key, Reason: fmt.Sprintf("文件夹「%s」已被其它镜像使用", mi.Folder)})
				continue
			}
			mi.ID = d.newUniqueID()
			mi.CreatedAt = m.timestampNow()
			d.Mirrors = append(d.Mirrors, mi)
			d.State[mi.ID] = newIdleState()
			res.Mirrors = append(res.Mirrors, MirrorView{Mirror: mi.clone(), State: newIdleState()})
			res.Imported++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	log.Printf("[mirror] imported %d mirror(s) from YAML, skipped %d", res.Imported, len(res.Skipped))
	return res, nil
}
