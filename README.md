# pc98golem

**無頭、決定性、可以當 Go 套件 import 的 PC-98 執行器**，為程式化觀測而寫。
與 [`dosgolem`](https://github.com/wicanr2/dosgolem) 同一套分層與判準，PC-98 版。

第一個案例：把 PC-98 版《Curse of the Azure Bonds》跑起來，在派曲常式攔截，
記錄每一次「哪一個 ECL 區塊放第幾首」，與 remake game pack 的綁定逐筆對拍。

為什麼要實跑而不是繼續靜態讀，見 [spec 001](docs/spec/001-scope-and-mvp.md)。

## 現在做到哪

| 期 | 內容 | 狀態 |
|---:|---|---|
| 1 | `internal/vfd` ＋ `internal/fat`：讀映像、列目錄、取檔 | **做完**（[spec 002](docs/spec/002-vfd-and-fat12.md)）|
| 2 | 借 dosgolem 的 CPU／DOS，加 PC-98 記憶體版圖 | 還沒 |
| 3 | GDC 與文字 VRAM 的最小子集 | 還沒 |
| 4 | YM2203 埠攔截 ＋ oracle，做對拍 | 還沒 |

## 用

```sh
tools/go.sh run ./cmd/pc98-disk /path/to/disk.fdd
tools/go.sh test ./...                      # 沒有映像的測試會 skip
PC98_DISK_DIR=/path/to/disks tools/go.sh test ./...
```

## 硬規則

- **不得散布原版素材。** 本儲存庫不含任何 `.fdd`、`.EXE`、`.DAX`。
  需要原版的測試**缺檔就 skip**，不做代用品。
- 建置與測試一律走 docker（`tools/go.sh`）。
- **SDD：spec 齊了才實作，只有標 `READY` 的規格可以動手。**
