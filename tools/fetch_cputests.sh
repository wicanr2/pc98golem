#!/usr/bin/env bash
# 抓 SingleStepTests/8088 v2 的測資到 testdata/（gitignore，761 MB）。
#
#   tools/fetch_cputests.sh            # 全部 323 個 opcode 檔 ＋ metadata
#   tools/fetch_cputests.sh 40 41 FF.2 # 只抓這幾個（開發時用）
#
# 檔名慣例：一般 opcode 是 `40.json.gz`，**群組 opcode 依 /r 拆成八個**
# （`80.0.json.gz` … `FF.7.json.gz`）。十六進位的字母是**大寫**。
#
# 語料的授權是它自己的（MIT），**不隨本儲存庫散布**。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$ROOT/testdata/8088"
BASE="https://raw.githubusercontent.com/SingleStepTests/8088/main/v2"
mkdir -p "$OUT"

fetch() {  # fetch <檔名>
  local f="$1"
  [[ -s "$OUT/$f" ]] && return 0
  echo "抓 $f"
  curl -fsSL --retry 3 -o "$OUT/$f.part" "$BASE/$f"
  mv "$OUT/$f.part" "$OUT/$f"
}

fetch metadata.json

if [[ $# -gt 0 ]]; then
  for op in "$@"; do fetch "$op.json.gz"; done
  exit 0
fi

# 全抓：opcode 清單就是 metadata 裡有測資的那些。用 GitHub API 列目錄，
# **不要自己列 00..FF**——有些 opcode 沒有測資檔（前綴、FPU），
# 自己列會在 8 個檔案上得到 404 而中止。
curl -fsSL "https://api.github.com/repos/SingleStepTests/8088/contents/v2" \
  | grep -o '"name": *"[0-9A-Fa-f.]*\.json\.gz"' \
  | sed 's/.*"\([0-9A-Fa-f.]*\.json\.gz\)"/\1/' \
  | while read -r f; do fetch "$f"; done

echo "完成：$(ls "$OUT"/*.json.gz 2>/dev/null | wc -l) 個 opcode 檔"
