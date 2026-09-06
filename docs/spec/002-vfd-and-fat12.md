# 002 — VFD 映像與 FAT12

狀態：**READY**（版面、壞磁區的處置、兩片映像的實測）
日期：2026-09-06
分期：spec 001 §6 的第 1 期

---

## 1. 為什麼從這一層開始

它**不需要 CPU、不需要原版程式碼就能自己證明自己對**：拿真的映像讀出目錄、
把檔案取出來，看開頭是不是該有的樣子（`MZ`、`TPOV`）。下層先站穩，
上層出錯的時候才分得出是誰錯。

## 2. VFD 的版面（由真映像量出來）

```
$0000  "VFD1.00" 加填充，共 220（$DC）位元組
$00DC  磁區表，每筆 12 位元組：
         +0 C   磁柱
         +1 H   磁頭
         +2 R   磁區號（1 起）
         +3 N   大小碼，位元組數 = 128 << N
         +4..7  旗標
         +8..11 這個磁區在檔案裡的偏移（小端 32 位元）
```

兩個坑：

1. **`C == $FF` 是「這個槽沒用到」，不是表結束。** 量到 4162 個槽 ÷ 77 磁柱
   ÷ 2 面 ≈ 每軌 27 槽，而實際每軌只有 8 個磁區——其餘全是 `$FF`。
   碰到第一個 `$FF` 就停，只會讀到第一軌的 8 個磁區。
2. **偏移 `$FFFFFFFF` 代表那個磁區當初讀不到。**

## 3. 不能假設磁區是連續的

一開始用「標頭之後就是連續磁區」讀，前面都對（開機磁區、FAT、根目錄都讀得出來），
`SETUP.EXE`、`MSCDRV.EXE` 也都是 `MZ`——**但 `GAME.EXE` 不是**。

原因是 Disk 1 有兩個磁區讀不到（LBA 55 與 673），它們在映像裡直接不存在，
所以之後每一個邏輯磁區都往前位移一格。`GAME.EXE` 宣告叢集 50，實際內容在 49。

> **「前面幾個檔案對得上」不足以證明版面對。** 那兩片的壞磁區剛好在
> 早期檔案之後，所以前四個檔案怎麼讀都對。判準要用**跨越壞磁區之後**的檔案。

## 4. 壞磁區要吵，不要補零

`Volume.Read` 對跨到壞磁區的檔案**回錯**，錯誤包住 `vfd.ErrAbsent`。

靜靜地補零會讓「這份映像不完整」看起來像「檔案沒問題」。手上這兩片的實測：

| 映像 | 讀不到的磁區 | 打到誰 |
|---|---|---|
| Disk 1 | LBA 55、673 | **`MSCDRV.EXE`**（音樂驅動）、`CED3.DAX` |
| Disk 2 | LBA 1208..1231（共 24 個）| 沒有檔案用到，全部可讀 |

**Disk 1 的 `MSCDRV.EXE` 是壞的。** CoAB 先前從「使用者持有的 PC-98 Disk 1」
抽出 `MSCDRV.EXE` 渲染 BGM——那份要另外確認來源，不能用這片映像重抽。

## 5. Disk 2 沒有合法 BPB

Disk 2 的 sector 0 是自訂 IPL（含 `AD&D -Curse of Th` 字樣），BPB 欄位是垃圾
（每磁區 49294、保留 1208）。版面退回 PC-98 2HD 的標準值：
每磁區 1024、每叢集 1、保留 1、FAT 2 份 × 2 磁區、根目錄 192 項。

`Volume.Geometry` 記著這是 `assumed-pc98-2hd` 還是 `boot-sector`，
**上層要據實轉述**——那是假設不是讀出來的。

判斷 BPB 合不合理**不能只看非零**：Disk 2 那組垃圾值每一項都非零，
只有整組一起看（每磁區要等於實際磁區大小、根目錄項數要能整除磁區）才擋得住。

## 6. 兩片的內容

| | 檔案 |
|---|---|
| Disk 1 | `IO98.SYS` `MEGDOS.SYS` `CONFIG.SYS` `LOADER.COM` `MSCDRV.EXE` `SETUP.EXE` **`GAME.EXE`** **`GAME.OVR`** `CURSE.CFG` `ITEMS` `8X8D.DAX` `SKY.DAX` `CHEAD.DAX` `CBODY.DAX` `COMSPR.DAX` `CED1..9.DAX` |
| Disk 2 | `BIGPIC` `BODY` `CPIC` `DUNGCOM` **`ECL`** **`GEO`** `HEAD` `ITEM` `MONCHA` `MONITM` `MONSPC` `PIC` `RANDCOM` `SPRIT` `TILES` `TITLE` `WALLDEF` `WILDCOM`（都是 `.DAX`）＋ `AUTOEXEC`／`CONFIG` 幾個變體 |

`GAME.OVR` 開頭是 `TPOV`，與 CoAB 文件記的 Turbo Pascal overlay 容器一致。
