package dos

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wicanr2/pc98golem/internal/cpu"
)

// 檔案服務：`AH=3Dh` 開、`3Eh` 關、`3Fh` 讀、`42h` seek。

type handle struct {
	name string
	path string
	f    *os.File
	size int64
}

// resolve 把遊戲組出來的路徑對到實際檔案。
//
// ⚠ **只認 basename。** 遊戲會自己組出 `A:\<垃圾>\DATA.PAK` 這種路徑
// （多磁片版的殘留），目錄部分對不上硬碟安裝版
// （`rich2/docs/re/006` §5：檔名是執行期組出來的，所以靜態找不到引用）。
//
// **大小寫不分**：原版是 DOS，檔名全大寫；玩家的目錄可能是小寫。
func (d *DOS) resolve(name string) string {
	base := name
	if i := strings.LastIndexAny(base, `\/:`); i >= 0 {
		base = base[i+1:]
	}
	if base == "" {
		return ""
	}
	direct := filepath.Join(d.Root, base)
	if st, err := os.Stat(direct); err == nil && !st.IsDir() {
		return direct
	}
	entries, err := os.ReadDir(d.Root)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.EqualFold(e.Name(), base) {
			return filepath.Join(d.Root, e.Name())
		}
	}
	return ""
}

// readCString 讀一個 NUL 結尾的字串。
func (d *DOS) readCString(seg, off uint16, limit int) string {
	addr := cpu.Addr(seg, off)
	out := make([]byte, 0, 64)
	for i := 0; i < limit; i++ {
		ch := d.M.Read8(addr + uint32(i))
		if ch == 0 {
			break
		}
		out = append(out, ch)
	}
	return string(out)
}

func (d *DOS) open(c *cpu.CPU) {
	name := d.readCString(c.Seg[cpu.DS], c.R[cpu.DX], 128)
	// 字元裝置：開 EMMXXXX0 成功 ＝ EMS 驅動存在（`docs/spec/007` §5）。
	// launcher 開完就關，不讀不寫；讀寫語意沒有證據，讀回 EOF、寫丟棄。
	if isEMMDevice(name) {
		h := d.nextHandle
		d.nextHandle++
		d.handles[h] = &handle{name: name}
		d.Opened = append(d.Opened, name)
		c.R[cpu.AX] = h
		clearCarry(c)
		return
	}
	path := d.resolve(name)
	if path == "" {
		d.Missing = append(d.Missing, name)
		c.R[cpu.AX] = 2 // File not found
		setCarry(c)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		d.Missing = append(d.Missing, name)
		c.R[cpu.AX] = 2
		setCarry(c)
		return
	}
	st, _ := f.Stat()
	h := d.nextHandle
	d.nextHandle++
	d.handles[h] = &handle{name: name, path: path, f: f, size: st.Size()}
	d.Opened = append(d.Opened, filepath.Base(path))
	c.R[cpu.AX] = h
	clearCarry(c)
}

func (d *DOS) close(c *cpu.CPU) {
	h, ok := d.handles[c.R[cpu.BX]]
	if !ok {
		c.R[cpu.AX] = 6 // Invalid handle
		setCarry(c)
		return
	}
	if h.f != nil {
		h.f.Close()
	}
	delete(d.handles, c.R[cpu.BX])
	clearCarry(c)
}

func (d *DOS) read(c *cpu.CPU) {
	bx, cx := c.R[cpu.BX], c.R[cpu.CX]
	if bx == 0 {
		d.readStdin(c, cx)
		return
	}
	h, ok := d.handles[bx]
	if !ok {
		c.R[cpu.AX] = 6
		setCarry(c)
		return
	}
	if h.f == nil { // 字元裝置（EMMXXXX0）：讀回 EOF
		c.R[cpu.AX] = 0
		clearCarry(c)
		return
	}
	buf := make([]byte, cx)
	n, _ := h.f.Read(buf)
	if n < 0 {
		n = 0
	}
	d.M.WriteBytes(cpu.Addr(c.Seg[cpu.DS], c.R[cpu.DX]), buf[:n])
	c.R[cpu.AX] = uint16(n)
	clearCarry(c)
}

// readStdin 是 `AH=3Fh` 讀 handle 0，也就是 BASIC 的 `INKEY$`
// （`rich2/docs/re/005`「輸入路徑」：這是唯一的鍵盤輪詢路徑，不是 `int 16h`）。
//
// ⚠ **不能回「讀到 0 個」。** 那等同 EOF，主程式會當成輸入結束、
// 還原中斷向量然後 exit——`RUN.EXE` 之前就死在這裡。
func (d *DOS) readStdin(c *cpu.CPU, want uint16) {
	if want == 0 {
		c.R[cpu.AX] = 0
		clearCarry(c)
		return
	}
	// **有資料就照呼叫端要的個數給。**
	//
	// ⚠ 舊版不管 `want` 一律只回 1 個位元組，於是**擴充鍵永遠送不進去**：
	// 方向鍵是 `00` ＋ 掃描碼兩個位元組，呼叫端一次要兩個的時候
	// 只拿到那個 `00`——而 `00` 也正是佇列空的時候餵的值（＝「沒按鍵」），
	// 所以它被當成「什麼都沒按」丟掉。症狀是**方向鍵安靜地沒有反應**，
	// 看起來像編碼寫錯，而不是像讀取實作漏了一個參數。
	n := int(want)
	if n > len(d.Stdin) {
		n = len(d.Stdin)
	}
	if n > 0 {
		d.M.WriteBytes(cpu.Addr(c.Seg[cpu.DS], c.R[cpu.DX]), d.Stdin[:n])
		d.Stdin = d.Stdin[n:]
		c.R[cpu.AX] = uint16(n)
		clearCarry(c)
		return
	}
	// 佇列空了。**預設還是要回「讀到 1 個」**——回 0 等同 EOF，
	// 主程式會還原中斷向量然後 exit。
	//
	// 例外是 StdinEmptyReadsZero：BASIC 的 `INKEY$` 空轉要看到**空字串**
	// 才會繼續等，餵 `00` 會讓它拿到 `CHR$(0)` 而立刻結束
	// （見該欄位的註解）。要讓「按任意鍵繼續」的畫面停住就打開它。
	if d.StdinEmptyReadsZero {
		c.R[cpu.AX] = 0
		clearCarry(c)
		return
	}
	d.M.WriteBytes(cpu.Addr(c.Seg[cpu.DS], c.R[cpu.DX]), []byte{d.StdinFill})
	c.R[cpu.AX] = 1
	clearCarry(c)
}

func (d *DOS) seek(c *cpu.CPU) {
	h, ok := d.handles[c.R[cpu.BX]]
	if !ok {
		c.R[cpu.AX] = 6
		setCarry(c)
		return
	}
	if h.f == nil { // 字元裝置不能 seek
		c.R[cpu.AX] = 1
		setCarry(c)
		return
	}
	off := int64(c.R[cpu.CX])<<16 | int64(c.R[cpu.DX])
	// CX:DX 是**有號**的：從結尾往回 seek 用負數。
	if c.R[cpu.CX]&0x8000 != 0 {
		off -= 1 << 32
	}
	pos, err := h.f.Seek(off, int(al(c)))
	if err != nil {
		c.R[cpu.AX] = 1
		setCarry(c)
		return
	}
	c.R[cpu.AX] = uint16(pos)
	c.R[cpu.DX] = uint16(pos >> 16)
	clearCarry(c)
}

// write 是 `AH=40h`。
//
// ⚠ **這是程式對我們說話的主要管道**，不是 `AH=09h`。BASIC runtime 的
// `PRINT` 與錯誤訊息都走這裡（handle 1／2），一次一小段。
// 沒接的話主控台是空的——看起來像「程式什麼都沒說」，
// 而實際上它正在印錯誤訊息（第一次跑通 CPU 之後就是這個症狀）。
//
// **寫檔一律不做。** 原版素材唯讀（`CLAUDE.md`），而 MVP-B 不需要存檔；
// 真的有人寫檔要看得到，所以記一筆而不是安靜地報成功。
func (d *DOS) write(c *cpu.CPU) {
	bx, cx := c.R[cpu.BX], c.R[cpu.CX]
	buf := make([]byte, cx)
	addr := cpu.Addr(c.Seg[cpu.DS], c.R[cpu.DX])
	for i := range buf {
		buf[i] = d.M.Read8(addr + uint32(i))
	}
	switch bx {
	case 1, 2: // stdout／stderr
		d.Console = append(d.Console, buf...)
	default:
		if h, ok := d.handles[bx]; ok {
			d.Wrote = append(d.Wrote, Write{Name: h.name, N: int(cx)})
		} else {
			d.Wrote = append(d.Wrote, Write{Name: fmt.Sprintf("handle %d", bx), N: int(cx)})
		}
	}
	c.R[cpu.AX] = cx
	clearCarry(c)
}
