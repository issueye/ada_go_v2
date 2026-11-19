// 离线仓库更新:repo.json 列出可用版本,按 semver 选择最新并复用 Upgrade。
package install

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/adag/ada-go/v2/version"
)

// RepoFile 是仓库索引文件名。
const RepoFile = "repo.json"

// RepoEntry 是仓库里的一个插件版本。
type RepoEntry struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Dir     string `json:"dir"` // 相对仓库根目录
	Digest  string `json:"digest,omitempty"`
}

type repoDoc struct {
	Entries []RepoEntry `json:"entries"`
}

// LoadRepo 读取仓库索引(不存在时返回空)。
func LoadRepo(repoRoot string) ([]RepoEntry, error) {
	data, err := os.ReadFile(filepath.Join(repoRoot, RepoFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("install: 读取仓库索引失败: %w", err)
	}
	var doc repoDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("install: 仓库索引损坏: %w", err)
	}
	return doc.Entries, nil
}

// LatestRepoEntry 返回某插件在仓库里的最高版本。
func LatestRepoEntry(entries []RepoEntry, id string) (RepoEntry, bool, error) {
	var candidates []RepoEntry
	for _, e := range entries {
		if e.ID == id {
			if _, err := version.Parse(e.Version); err != nil {
				return RepoEntry{}, false, fmt.Errorf("install: 仓库版本号非法 %q: %w", e.Version, err)
			}
			candidates = append(candidates, e)
		}
	}
	if len(candidates) == 0 {
		return RepoEntry{}, false, nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, _ := version.Parse(candidates[i].Version)
		b, _ := version.Parse(candidates[j].Version)
		return version.Compare(a, b) < 0
	})
	return candidates[len(candidates)-1], true, nil
}

// resolveRepoDir 把仓库条目目录限制在仓库根之下。
func resolveRepoDir(repoRoot, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("install: 仓库条目缺少 dir")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("install: 仓库 dir 只允许相对路径: %s", rel)
	}
	rootAbs, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", fmt.Errorf("install: 解析仓库根失败: %w", err)
	}
	abs, err := filepath.Abs(filepath.Join(rootAbs, filepath.Clean(rel)))
	if err != nil {
		return "", fmt.Errorf("install: 解析仓库目录失败: %w", err)
	}
	inside, err := filepath.Rel(rootAbs, abs)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("install: 仓库 dir 不能逃逸仓库根: %s", rel)
	}
	return abs, nil
}

// PreviewRepoUpdate 只校验仓库里该插件的最新版本,不落盘(供安装同意预览)。
func PreviewRepoUpdate(repoRoot, id string, opts Options) (*Info, error) {
	entries, err := LoadRepo(repoRoot)
	if err != nil {
		return nil, err
	}
	latest, ok, err := LatestRepoEntry(entries, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("install: 仓库里没有插件 %q", id)
	}
	dir, err := resolveRepoDir(repoRoot, latest.Dir)
	if err != nil {
		return nil, err
	}
	info, err := VerifyDir(dir, opts)
	if err != nil {
		return nil, err
	}
	if info.ID != id {
		return nil, fmt.Errorf("install: 仓库条目 id %q 与清单 id %q 不一致", id, info.ID)
	}
	if latest.Digest != "" && info.Digest != latest.Digest {
		return nil, fmt.Errorf("install: 摘要不匹配(期望 %s,实际 %s)", latest.Digest, info.Digest)
	}
	return info, nil
}

// UpdateFromRepo 从离线仓库安装最新版本;已安装版本不旧于仓库最新时拒绝。
func UpdateFromRepo(repoRoot, id, destRoot string, opts Options) (*Info, error) {
	if id == "" {
		return nil, fmt.Errorf("install: 插件 id 不能为空")
	}
	entries, err := LoadRepo(repoRoot)
	if err != nil {
		return nil, err
	}
	latest, ok, err := LatestRepoEntry(entries, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("install: 仓库里没有插件 %q", id)
	}

	installed, err := List(destRoot)
	if err != nil {
		return nil, err
	}
	newest := ""
	var newestVer version.Version
	for _, e := range installed {
		if e.ID != id {
			continue
		}
		v, err := version.Parse(e.Version)
		if err != nil {
			return nil, fmt.Errorf("install: 已安装版本号非法 %q: %w", e.Version, err)
		}
		if newest == "" || version.Compare(v, newestVer) > 0 {
			newest, newestVer = e.Version, v
		}
	}
	if newest == "" {
		return nil, fmt.Errorf("install: 插件 %q 未安装,请使用 install", id)
	}
	cmp, err := compareVersionStrings(latest.Version, newest)
	if err != nil {
		return nil, err
	}
	switch {
	case cmp == 0:
		return nil, fmt.Errorf("install: 插件 %q 已是最新版本 %s", id, newest)
	case cmp < 0:
		return nil, fmt.Errorf("install: 仓库版本 %s 低于已安装版本 %s,拒绝降级", latest.Version, newest)
	}

	dir, err := resolveRepoDir(repoRoot, latest.Dir)
	if err != nil {
		return nil, err
	}
	preview, err := VerifyDir(dir, opts)
	if err != nil {
		return nil, err
	}
	if preview.ID != id {
		return nil, fmt.Errorf("install: 仓库条目 id %q 与清单 id %q 不一致", id, preview.ID)
	}
	if latest.Digest != "" && preview.Digest != latest.Digest {
		return nil, fmt.Errorf("install: 摘要不匹配(期望 %s,实际 %s)", latest.Digest, preview.Digest)
	}
	info, err := Upgrade(dir, destRoot, opts)
	if err != nil {
		return nil, err
	}
	return info, nil
}
