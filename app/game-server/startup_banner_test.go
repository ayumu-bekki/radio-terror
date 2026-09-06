package main

import (
	"strings"
	"testing"
)

// TestDescribeSecretWords は起動ログの秘密ワード表示を確かめる。
//
// 目的は**設定が意図どおり読めているかを起動ログだけで判別できる**こと。
// 「設定を直したのにコンテナが古いままで反映されていない」という切り分けに
// 時間を取られたため、件数が一目で分かる形にしてある。
//
// 秘密ワードは運営外秘なので**値そのものは出さない**。
func TestDescribeSecretWords(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"カンマ区切りで2語", "でんぱ,電波", "2 word(s): で…(3), 電…(2)"},
		{"1語のみ", "でんぱ", "1 word(s): で…(3)"},
		{"空白入り", " でんぱ , 電波 ", "2 word(s): で…(3), 電…(2)"},
		{"未設定", "", "NOT SET (キルスイッチは無効)"},
		{"空要素だけ", " , ", "NOT SET (キルスイッチは無効)"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := describeSecretWords(c.raw); got != c.want {
				t.Errorf("describeSecretWords(%q) = %q, want %q", c.raw, got, c.want)
			}
		})
	}
}

// TestDescribeSecretWordsHidesValue は秘密ワードの値がログへ出ないことを確かめる。
// ログは画面共有や記録に残るため、全文が出てはいけない。
func TestDescribeSecretWordsHidesValue(t *testing.T) {
	const secret = "でんぱ"
	got := describeSecretWords(secret + ",電波")

	if strings.Contains(got, secret) {
		t.Errorf("秘密ワードがそのままログに出ている: %q", got)
	}
	if strings.Contains(got, "電波") {
		t.Errorf("秘密ワードがそのままログに出ている: %q", got)
	}
}

// TestBuildIdentityAlwaysReports は起動ログのビルド識別子が
// 常に何かを返すことを確かめる (空だと切り分けに使えない)。
func TestBuildIdentityAlwaysReports(t *testing.T) {
	got := buildIdentity()
	if got == "" {
		t.Fatal("buildIdentity が空")
	}
	// Go のバージョンは必ず入る
	if !strings.Contains(got, "go1.") {
		t.Errorf("Go バージョンが含まれていない: %q", got)
	}
}

// TestBuildIdentityShortensFullHash は commit id を短縮しつつ、
// `+dirty` の印を残すことを確かめる。
//
// commit id は `go build` が埋め込む40桁のフルハッシュ。読みやすさのため
// 12桁へ短縮するが、**未コミットの変更が入ったビルドかどうか**は
// 切り分けに必要な情報なので消さない。
func TestBuildIdentityShortensFullHash(t *testing.T) {
	got := buildIdentity()

	// go test では vcs 情報が入らないことがあるため、形式だけ確認する
	if !strings.Contains(got, "go1.") {
		t.Errorf("Go バージョンが含まれていない: %q", got)
	}
	// 40桁のフルハッシュがそのまま出ていないこと
	for _, field := range strings.Split(got, " / ") {
		bare := strings.TrimSuffix(field, "+dirty")
		if len(bare) == 40 {
			t.Errorf("フルハッシュが短縮されていない: %q", got)
		}
	}
}
