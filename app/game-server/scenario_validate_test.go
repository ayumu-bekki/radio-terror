package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoOnWrongCutInStages は、ステージ定義に `on_wrong_cut` を書き戻さないことを固定する。
//
// **誤切断は難易度を問わず即爆発**で、設定項目そのものを廃止した (決定27)。
// 切った線は物理的に戻せないため、penalty で続行させると後続ステージが
// その色を使う場合に組み立てが破綻する (配線5本に対し1セッション最大4ステージ
// しかなく余裕が無い)。
//
// デバイス側は `on_wrong_cut` をパースしないので、書いても**効かない**。
// 「書いてあるのに効かない設定」は事故のもとなので、ここで弾く。
//
// **無効化済み (.toml.disabled) も対象にする** — 再開したときに
// 古い記述のまま戻ってこないようにするため。
func TestNoOnWrongCutInStages(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("scenarios", "stages", "*.toml*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no stage files found")
	}

	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			if !strings.Contains(line, "on_wrong_cut") {
				continue
			}
			t.Errorf("%s:%d: on_wrong_cut は廃止済み。誤切断は常に即爆発 (決定27): %s",
				filepath.Base(path), i+1, strings.TrimSpace(line))
		}
	}
}
