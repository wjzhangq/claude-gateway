package classify

import "github.com/wjzhangq/claude-gateway/config"

// DefaultConfig returns the built-in rule tables: the suffix → code-direction
// map and the documentation-suffix set. Scoring defaults to the same values as
// config.defaultConfig so a zero-config classify.Config is still usable in tests.
func DefaultConfig() Config {
	return Config{
		DirBySuffix: map[string]string{
			// 前端
			".js": "前端", ".jsx": "前端", ".ts": "前端", ".tsx": "前端",
			".vue": "前端", ".css": "前端", ".scss": "前端", ".less": "前端",
			".html": "前端", ".htm": "前端",
			// 后端
			".go": "后端", ".java": "后端", ".py": "后端", ".rb": "后端",
			".php": "后端", ".rs": "后端", ".cs": "后端", ".scala": "后端",
			".cpp": "后端", ".cc": "后端", ".c": "后端", ".h": "后端", ".hpp": "后端",
			// 移动端
			".swift": "移动端", ".kt": "移动端", ".dart": "移动端", ".m": "移动端",
			// 数据
			".sql": "数据", ".csv": "数据", ".parquet": "数据",
			// 脚本
			".sh": "脚本", ".bash": "脚本", ".zsh": "脚本", ".ps1": "脚本",
			// 运维
			".tf": "运维", ".yaml": "运维", ".yml": "运维", ".toml": "运维",
		},
		DocSuffixes: map[string]bool{
			".md": true, ".markdown": true, ".txt": true, ".rst": true,
			".adoc": true, ".doc": true, ".docx": true, ".pdf": true,
		},
		Score: ScoreWeights{
			NonWork:       0.7,
			Volume:        0.3,
			BaselineTasks: 60,
			Threshold:     0.5,
		},
	}
}

// FromAnalyzeConfig builds a classify.Config from the gateway's AnalyzeConfig.
// The suffix/doc tables come from DefaultConfig (not configurable); the scoring
// weights come from config so an operator can tune them without a rebuild.
func FromAnalyzeConfig(a config.AnalyzeConfig) Config {
	c := DefaultConfig()
	c.Score = ScoreWeights{
		NonWork:       a.Score.NonWork,
		Volume:        a.Score.Volume,
		BaselineTasks: a.Score.BaselineTasks,
		Threshold:     a.Score.Threshold,
	}
	return c
}
