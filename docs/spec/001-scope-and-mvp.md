# 001 — 範圍與 MVP

狀態：**READY**（§1..§5）；DRAFT（§6 之後的分期）
日期：2026-09-06
依據：[`dosgolem`](https://github.com/wicanr2/dosgolem) 的 `docs/spec/001`
（同一套分層與判準，PC-98 版）

---

## 1. 這個東西是什麼

**一個無頭、決定性、可以當 Go 套件 import 的 PC-98 執行器**，
目的只有一個：讓金盒子 remake 的對拍從「隔著模擬器看畫面」變成
「在同一個行程裡讀原版的記憶體」。

它**不是**通用 PC-98 模擬器，也不打算變成。判準是：

> 跑得動 PC-98 版《Curse of the Azure Bonds》的 `GAME.EXE`，
> 而且在 remake 關心的觀測點上跑出與 DOSBox-X 一致的狀態。

其他 PC-98 程式跑不跑得動不在範圍內；跑得動是副產物，不是目標。

## 2. 為什麼要它——現成的解決不了什麼

CoAB 的 remake 已經有 PC-98 的素材與靜態證據：`GAME.EXE` 帶完整 Borland
除錯符號，音樂驅動 `MSCDRV.EXE` 已經抽出來能渲染，派曲常式（`sub_18AA7`
／`sub_18A44`）也讀過了。**缺的是執行期的證據**：

> 哪一個 ECL 區塊、在哪一個時機，實際把哪一個 `MUSICNUM` 寫下去。

那個問題靜態讀不出來——同一支派曲常式被很多地方呼叫，曲號是呼叫端算出來的。

| 方案 | 為什麼不夠 |
|---|---|
| DOSBox-X | PC-98 支援是有的，但除錯器是互動介面，不是可程式化 API；要做「跑到某一格、讀某個變數、逐筆比對」很痛 |
| Neko Project II | 同上，而且更不容易嵌 |
| 靜態反組譯 | 已經做了。它給得出**派曲的規則**，給不出**每一格實際放哪一首** |

**DOSBox-X 留著當交叉 oracle。** 本專案算出來的每一個狀態，
都要拿它驗過才算數——這一條與 dosgolem 相同。

> **這個教訓有代價**：Pool of Radiance 那一側先用靜態讀，得到
> 「原版三個情境都有音樂」的結論；把 C64 版真的跑起來錄音量 RMS 才發現
> **只有標題有音樂**，地圖與戰鬥是靜的。「有派曲的包裝」不等於「有音樂」，
> 而那個差別**只有實跑看得出來**。

## 3. 第一個案例（驗收條件）

跑 PC-98 版 CoAB，在派曲常式攔截，記錄每一次 `(ECL 區塊, MUSICNUM)`，
與 remake game pack 的 `FindMusicBinding` **逐筆對拍**。

通過條件：

1. 原版實際出現過的 `(區塊, 曲號)` 組合，game pack 都對得上；
2. game pack 宣告的組合，若原版跑到那一格卻放別的曲子，要列出來；
3. 兩邊的差異一律**列出來**，不是靜靜地放過。

**這不是「跑得動就算」**：跑得動是前提，對得上才是驗收。

## 4. 分層（照 dosgolem，差在 machine 那一層）

```
internal/cpu/      CPU 核心。可直接用 dosgolem 的（8086／V30 相容子集）
internal/dos/      MS-DOS 服務。PC-98 跑的也是 MS-DOS，INT 21h 大致共用
internal/machine/  **這一層才是 PC-98 專屬**：記憶體版圖、GDC（µPD7220）、
                   文字／圖形 VRAM、PIT、鍵盤、YM2203（OPN）埠
internal/vfd/      VFD／FDD 磁碟映像
internal/fat/      FAT12 檔案系統
oracle/            對外的 Go API：Load／RunUntil／OnCall／Search
apps/coab/         CoAB 專屬：位址、狀態、攔截點
cmd/probe/         通用探針
```

分層的理由與 dosgolem 相同：**下層要能自己證明自己對**。
`internal/vfd` 與 `internal/fat` 不需要 CPU、不需要原版程式碼就能驗——
拿真的磁碟映像讀出目錄，對得上就是對得上。

## 5. `[HARD]` 硬規則

工作規則集中在 [`CLAUDE.md`](../../CLAUDE.md)，這裡只列與範圍直接相關的兩條：

- **不得散布原版素材。** 本儲存庫不含任何 `.fdd`、`.EXE`、`.DAX`。
  需要原版的測試**缺檔就 skip**，不做代用品——安靜的替代品會讓
  「還沒做完」看起來像做完了。
- **SDD：spec 齊了才實作，只有標 `READY` 的規格可以動手。**

## 6. 分期（DRAFT）

| 期 | 內容 | 能證明什麼 |
|---:|---|---|
| 1 | `internal/vfd` ＋ `internal/fat`：讀映像、列目錄、取檔 | 磁碟讀對了（拿真映像驗）|
| 2 | 借 dosgolem 的 CPU／DOS，加 PC-98 記憶體版圖，跑到 `GAME.EXE` 進入點 | 載入器對了 |
| 3 | GDC 與文字 VRAM 的最小子集，跑到第一個畫面 | 不當機、畫得出東西 |
| 4 | YM2203 埠攔截 ＋ `oracle.OnCall`，做 §3 的對拍 | **驗收** |

每一期都要先有自己的 spec 標 `READY` 才動手。
