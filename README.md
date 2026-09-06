# pc98golem

**無頭、決定性、可以當 Go 套件 import 的 PC-98 執行器**，為程式化觀測而寫。
與 [`dosgolem`](https://github.com/wicanr2/dosgolem) 同一套分層與判準，PC-98 版。

第一個案例：把 PC-98 版《Pool of Radiance》跑起來，在音樂驅動的 `INT 7Eh` 攔截，
記錄每一次「哪一個 ECL 區塊放第幾首」，與 remake game pack 的綁定逐筆對拍。

為什麼要實跑而不是繼續靜態讀，見 [spec 001](docs/spec/001-scope-and-mvp.md)。

## 現在做到哪

| 期 | 內容 | 狀態 |
|---:|---|---|
| 1 | `internal/vfd`／`internal/d88` ＋ `internal/fat`：讀映像、列目錄、取檔 | **做完**（[spec 002](docs/spec/002-vfd-and-fat12.md)、[spec 004](docs/spec/004-d88-container.md)）|
| 1.5 | 靜態讀出 PC-98 版 Pool 的派曲規則 | **做完**（[spec 005](docs/spec/005-pool-pc98-music.md)：15 首、29 個區塊的對照表）|
| 2 | **音訊**：軟體音源 BIOS、跑原版音樂驅動、YM2203 合成 | **做完**（[spec 006](docs/spec/006-audio-and-sound-bios.md)）|
| 3 | GDC 與文字 VRAM 的最小子集 | 還沒 |
| 4 | 跑整支 `GAME.EXE`（GDC、文字 VRAM、磁碟）| 還沒 |

**區域配樂表已經實跑驗過了**：`apps/pool` 把 `GAME.EXE` 的區域配樂程序直接
叫起來，29 個 ECL 區塊逐筆對上靜態反組譯讀出來的表。那支程序是純查表，
不需要畫面也不需要磁碟。

音訊那一輪的結果：把原版的 `MSCDRV.EXE` 在模擬的 CPU 上跑起來，攔它交給
音源 BIOS 的演奏資料，再用純 Go 的 OPN 合成成 WAV。抽出來的資料與
`golden-box-remake-engine` 靜態解析的**逐筆相同（90／90 個聲道）**——
兩個來源完全獨立，這是格式讀對了的證據。

## 用

```sh
tools/go.sh run ./cmd/pc98-disk /disks/disk.fdd            # VFD 與 D88 都認
MSCDRV_DIR=<驅動目錄> tools/go.sh run ./cmd/pc98-music \
    -driver /driver/MSCDRV.EXE -out /src/.out              # 跑驅動、合成 WAV
MSCDRV_DIR=<驅動目錄> tools/go.sh run ./cmd/pool-cues \
    -game /driver/GAME.EXE                                 # 區域配樂表（實跑）
tools/go.sh run ./cmd/pc98-disk -extract all -out /src/out /disks/disk.d88
tools/go.sh test ./...                                     # 沒有映像的測試會 skip
PC98_DISK_DIR=/path/to/coab-disks tools/go.sh test ./...   # 掛進容器的 /disks
PC98_POOL_DISK_DIR=/path/to/pool-d88 tools/go.sh test ./...
```

## 硬規則

- **不得散布原版素材。** 本儲存庫不含任何 `.fdd`、`.EXE`、`.DAX`。
  需要原版的測試**缺檔就 skip**，不做代用品。
- 建置與測試一律走 docker（`tools/go.sh`）。
- **SDD：spec 齊了才實作，只有標 `READY` 的規格可以動手。**
