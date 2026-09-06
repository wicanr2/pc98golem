# pc98golem — 給 Claude 的專案規則

## 這是什麼

**無頭、決定性、可以當 Go 套件 import 的 PC-98 執行器**，為程式化觀測而寫。
與 [`dosgolem`](https://github.com/wicanr2/dosgolem) 同一套分層與判準，PC-98 版；
`internal/cpu`／`internal/dos`／`internal/machine`／`oracle` 是從那邊**複製**
過來的（來源與同步紀律見 `docs/spec/003`）。

第一個案例是 PC-98 版《Pool of Radiance》的配樂對拍：派曲規則已經靜態讀完
（`docs/spec/005`），缺的是實跑證據。範圍與驗收條件見 `docs/spec/001`。

## 動手前

1. 讀 `docs/spec/001-scope-and-mvp.md`（範圍與 MVP）。
2. **SDD：spec 齊了才實作。只有標 `READY` 的規格可以動手。**
   反組譯／量測 → 規格 → 才寫程式。

## `[HARD]` 硬規則

- **不得散布原版素材。** 本儲存庫不含任何 `.fdd`、`.EXE`、`.DAX`。
  需要原版的測試**缺檔就 skip**，不用自製代用品——安靜的替代品會讓
  「還沒做完」看起來像做完了。
- **建置與測試一律走 docker**（`tools/go.sh`），不裝到系統環境。
  只清理自己建立的 container；禁止任何 `docker image/system/volume/builder prune`
  或 `rmi`。
- **git 身分一律 `wicanr2@gmail.com`。** 進 repo 先看 `git config user.email`，
  再跑一次 `git log --format=%ae | sort -u` 看歷史。
- **測試語料不進版控。** 用 `tools/fetch_cputests.sh` 抓到 `testdata/`。
- **CPU 的驗收判準是「全部通過」，不是「大部分通過」。**
  CPU 的錯不會報錯，只會讓上層在幾百萬個指令之後畫錯一個像素。
- **推論標籤要誠實**：confirmed／強證據／假說／未知。

## 這個專案已經踩過的坑

寫在規格裡，不要再踩一次：

- **「前面幾個檔案讀對了」證明不了版面對。** VFD 那一層用「連續磁區」讀，
  開機磁區、FAT、根目錄、前四個檔案全都對，`GAME.EXE` 才露餡（spec 002 §3）。
  判準要用**跨越壞區之後**的資料。
- **壞掉的東西要吵。** 補零會讓「映像不完整」看起來像「檔案沒問題」（spec 002 §4）。
- **判斷一組值合不合理，不能只看非零。** Disk 2 的垃圾 BPB 每一項都非零
  （spec 002 §5）。
- **「有派曲的包裝」不等於「有音樂」。** 那個差別只有實跑看得出來（spec 001 §2）。
- **對拍抓到的三個「看起來對、其實錯」**：曲號 0 起算（減一在遊戲那邊）、
  區塊位址記的是資料起點不是標頭、安裝路徑已經先抽掉一個區塊。
  三個的症狀都不是報錯（spec 006 §5）。

## 分層

```
internal/cpu/      CPU 核心。不認識 DOS、不認識畫面、不認識檔案（複製自 dosgolem）
internal/dos/      MS-DOS 與 BIOS 服務（複製自 dosgolem）
internal/machine/  記憶體、載入器（複製自 dosgolem；**PC-98 的分歧會加在這裡**）
internal/soundbios/ NEC 音源 BIOS（INT D2h）的軟體替身
internal/mscdrv/   跑 MSCDRV 音樂驅動，抽演奏資料，合成
internal/opn/      YM2203 合成子集（近似，不是週期精確）
internal/disk/     磁碟容器的共同介面（ErrAbsent 與 Image）
internal/vfd/      VFD 磁碟映像
internal/d88/      D88 磁碟映像
internal/fat/      FAT12
oracle/            對外的 Go API（複製自 dosgolem）
cmd/               工具
docs/spec/         規格，標 DRAFT／READY
tools/             docker 包裝與語料抓取
```
