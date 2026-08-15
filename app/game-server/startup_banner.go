package main

import (
	"fmt"
	"log"
	"runtime"
	"runtime/debug"
	"strings"
)

// logStartupBanner は起動時に「どのビルドが、どの設定で動いているか」を出す。
//
// 実運用で**設定を直したのに反映されない**(コンテナが古いバイナリのまま)
// という切り分けに時間を取られた。ログを見るだけで
// 「バイナリが新しいか」「設定が読めているか」が判別できるようにする。
//
// 秘密ワードは**そのまま出さない**。運営外秘であり、ログは画面共有や
// 記録に残るため。件数と各語の長さ・先頭1文字だけを出して、
// 「意図した数の語が読めているか」を確認できるようにする。
func logStartupBanner(configPath string, cfg *Config) {
	log.Printf("[boot] commit: %s", buildIdentity())
	log.Printf("[boot] config: %s", configPath)
	log.Printf("[boot] manager secret words: %s", describeSecretWords(cfg.Manager.SecretWord))
}

// buildIdentity は commit id と Go バージョンを返す。
//
// commit id は `go build` が `.git` から自動で埋め込む (vcs.revision)。
// Docker ビルドでも取れるよう、compose の build context を
// **リポジトリルート**にして `.git` を含めている (compose.yaml / Dockerfile)。
//
// **どのソースから作られたバイナリか**が commit id で一意に分かるので、
// 「直したのに反映されない」の切り分けはこれで足りる。
// 未コミットの変更が入ったビルドには `+dirty` が付く。
func buildIdentity() string {
	commit := "unknown"
	modified := ""

	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				commit = setting.Value
			}
			if setting.Key == "vcs.modified" && setting.Value == "true" {
				modified = "+dirty"
			}
		}
	}
	// vcs.revision は40桁のフルハッシュで入る。読みやすさのため短縮するが、
	// build args 側で付く "-dirty" のような接尾辞は削らない
	// (未コミットの変更が入ったビルドかどうかは残す必要がある)。
	if len(commit) == 40 {
		commit = commit[:12]
	}

	return strings.Join([]string{commit + modified, runtime.Version()}, " / ")
}

// describeSecretWords は秘密ワードの設定状況を**値を伏せて**説明する。
//
// 例: `2 word(s): "で…"(3), "電…"(2)`
// 件数が想定と違えば、カンマ区切りの設定が効いていないと分かる。
func describeSecretWords(raw string) string {
	words := parseSecretWords(raw)
	if len(words) == 0 {
		return "NOT SET (キルスイッチは無効)"
	}

	parts := make([]string, 0, len(words))
	for _, word := range words {
		runes := []rune(word)
		parts = append(parts, fmt.Sprintf("%s…(%d)", string(runes[0]), len(runes)))
	}
	return fmt.Sprintf("%d word(s): %s", len(words), strings.Join(parts, ", "))
}
