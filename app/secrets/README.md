# secrets

Gemini Enterprise Agent Platform (旧 Vertex AI) の
サービスアカウントキー (JSON) を置く場所。

**このディレクトリの中身はコミットされない** (README.md を除く)。
鍵は `private_key` を含み、持っている者は誰でもプロジェクトの
Gemini Enterprise Agent Platform を呼べる (= 課金が発生する)。

発行手順と配置方法は `docs/gemini_enterprise_setup.md` を参照。

```bash
cp ~/Downloads/radio-terror-xxxxx.json app/secrets/geap-sa.json
chmod 600 app/secrets/geap-sa.json
export GOOGLE_APPLICATION_CREDENTIALS=$(pwd)/app/secrets/geap-sa.json
```
