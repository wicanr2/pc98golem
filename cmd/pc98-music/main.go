// pc98-music 把使用者本機的 MSCDRV 驅動**跑起來**，抽出演奏資料並合成 WAV。
//
// 原版驅動與輸出的音訊都不進版控。
//
//	tools/go.sh run ./cmd/pc98-music -driver /driver/MSCDRV.EXE -out /src/.out
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/wicanr2/pc98golem/internal/mscdrv"
)

func main() {
	driverPath := flag.String("driver", "", "MSCDRV 執行檔")
	outDir := flag.String("out", ".", "輸出目錄")
	channels := flag.Int("channels", 6, "每首的聲道數")
	fmChannels := flag.Int("fm", 3, "前幾個聲道走 FM")
	tracks := flag.Int("tracks", 15, "曲數")
	track := flag.Int("track", 0, "只做這一首（1 起算），0 代表全部")
	maxBlocks := flag.Int("max-blocks", 512, "每個聲道最多抽幾個區塊")
	seconds := flag.Float64("seconds", 90, "每首的長度上限")
	rate := flag.Int("rate", 44100, "取樣率")
	dumpBlocks := flag.Bool("dump-blocks", false, "輸出區塊 JSON，不合成")
	flag.Parse()

	if *driverPath == "" {
		fmt.Fprintln(os.Stderr, "用法：pc98-music -driver MSCDRV.EXE [-out 目錄]")
		os.Exit(2)
	}
	image, err := os.ReadFile(*driverPath)
	if err != nil {
		fail(err)
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fail(err)
	}
	for index := 0; index < *tracks; index++ {
		if *track != 0 && index != *track-1 {
			continue
		}
		if err := one(image, index, *outDir, *channels, *fmChannels,
			*maxBlocks, *seconds, *rate, *dumpBlocks); err != nil {
			fail(err)
		}
	}
}

func one(
	image []byte, index int, outDir string,
	channels, fmChannels, maxBlocks int, seconds float64, rate int, dump bool,
) error {
	// 每一首都用一台乾淨的機器：驅動的工作區有跨曲殘留，共用會讓
	// 「這一首的內容」摻進上一首的狀態。
	driver, err := mscdrv.Load(image, channels)
	if err != nil {
		return err
	}
	result, err := driver.Play(index, maxBlocks)
	if err != nil {
		return err
	}
	if dump {
		type blockJSON struct {
			Offset uint16 `json:"offset"`
			Bytes  string `json:"bytes"`
		}
		out := make([][]blockJSON, len(result.Channels))
		for channel, blocks := range result.Channels {
			for _, block := range blocks {
				out[channel] = append(out[channel], blockJSON{
					Offset: block.Offset, Bytes: fmt.Sprintf("%x", block.Bytes),
				})
			}
		}
		body, err := json.MarshalIndent(map[string]any{
			"track": index + 1, "truncated": result.Truncated,
			"stuck": result.Stuck, "channels": out,
		}, "", "  ")
		if err != nil {
			return err
		}
		path := filepath.Join(outDir, fmt.Sprintf("blocks-%02d.json", index+1))
		if err := os.WriteFile(path, body, 0o644); err != nil {
			return err
		}
		fmt.Printf("第 %2d 首 → %s\n", index+1, path)
		return nil
	}

	events, unknown, err := result.Events(fmChannels)
	if err != nil {
		return err
	}
	samples, length, err := mscdrv.Render(events, fmChannels, mscdrv.RenderOptions{
		SampleRate: float64(rate), MaxSeconds: seconds,
	})
	if err != nil {
		return err
	}
	path := filepath.Join(outDir, fmt.Sprintf("track-%02d.wav", index+1))
	if err := os.WriteFile(path, mscdrv.WriteWAV(samples, rate), 0o644); err != nil {
		return err
	}
	fmt.Printf("第 %2d 首 → %s（%.1f 秒，%d 個事件）", index+1, path, length, len(events))
	if result.Truncated {
		fmt.Printf("，**抽取被上限截斷**")
	}
	fmt.Println()
	if len(unknown) > 0 {
		fmt.Printf("        未解命令：%v\n", unknown)
	}
	for _, stuck := range result.Stuck {
		fmt.Printf("        %s\n", stuck)
	}
	return nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
