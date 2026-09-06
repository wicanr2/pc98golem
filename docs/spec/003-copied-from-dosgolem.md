# 003 — 從 dosgolem 複製過來的部分

狀態：**READY**
日期：2026-09-06

---

## 1. 複製而不是相依

`internal/cpu`、`internal/dos`、`internal/machine`、`oracle` 四層是從
[`dosgolem`](https://github.com/wicanr2/dosgolem) **複製**過來的，不是 import。

| 來源 | |
|---|---|
| 儲存庫 | `github.com/wicanr2/dosgolem` |
| 分支 | `logh3-com-support` |
| commit | `3b916debaaf6dd416e781b1df050e0362516991f` |
| 日期 | 2026-09-06 |

複製的理由：**PC-98 要改的正好是最底下那幾層。** 記憶體版圖、中斷向量、
I/O 埠、顯示子系統全都不一樣；用 import 的話，每加一個 PC-98 的分歧就要在
dosgolem 那邊開一個它用不到的抽象。兩邊各自演化比較誠實，代價是 CPU 的修正
要手動同步——那個代價寫在下面。

## 2. 同步的紀律

CPU 那一層是**兩邊共用的真相**（8086／V30 的行為與平台無關），所以：

- **dosgolem 的 CPU 修正要搬過來**，反之亦然。
- 搬的時候更新本規格的 commit 欄位，並在 commit message 裡寫清楚搬了哪一筆。
- **不要在 pc98golem 這邊「順手改」CPU 的語意**。真的要改，
  先確認 dosgolem 那邊的測試語料也同意——判準是 SingleStepTests 全過，
  不是「大部分過」。

CPU 的錯不會報錯，只會讓上層在幾百萬個指令之後畫錯一個像素。

## 3. 測試語料

`internal/cpu` 的驗收語料（SingleStepTests，761 MB）**不進版控**，
用 `tools/fetch_cputests.sh` 抓到 `testdata/`。沒有語料時
`TestSingleStep` **會 skip 並說明原因**，不會安靜地通過。

## 4. 授權

dosgolem 與 pc98golem 都是 RRSAL-1.0，同一個權利人，所以複製沒有授權問題。
`LICENSE` 一併複製過來，只改了「適用作品」那一行。
