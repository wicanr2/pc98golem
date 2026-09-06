// pool-cues 把 PC-98 版《Pool of Radiance》的「區域配樂」程序直接叫起來，
// 對每一個 ECL 區塊印出它派哪一首曲子。
//
// 這是**跑原版的碼**得到的結果，不是讀反組譯。原版執行檔不進版控。
//
//	MSCDRV_DIR=<目錄> tools/go.sh run ./cmd/pool-cues -game /driver/GAME.EXE
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/wicanr2/pc98golem/apps/pool"
)

func main() {
	gamePath := flag.String("game", "", "GAME.EXE")
	mode := flag.Int("mode", 3, "遊戲模式（1、5、7 有自己的曲子，會擋掉區域配樂）")
	asJSON := flag.Bool("json", false, "輸出 JSON")
	flag.Parse()
	if *gamePath == "" {
		fmt.Fprintln(os.Stderr, "用法：pool-cues -game GAME.EXE")
		os.Exit(2)
	}
	image, err := os.ReadFile(*gamePath)
	if err != nil {
		fail(err)
	}
	game, err := pool.Load(image)
	if err != nil {
		fail(err)
	}
	// 原版沒有編號 12 的 ECL 區塊。
	areas := make([]int, 0, 29)
	for area := 0; area <= 29; area++ {
		if area != 12 {
			areas = append(areas, area)
		}
	}
	results, err := game.SweepAreaMusic(areas, byte(*mode))
	if err != nil {
		fail(err)
	}
	if *asJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(results)
		return
	}
	// 反過來列：一首曲子對到哪些區塊，比較看得出形狀。
	byTrack := map[byte][]int{}
	for _, result := range results {
		byTrack[result.Song] = append(byTrack[result.Song], result.Area)
	}
	fmt.Printf("模式 %d，%d 個 ECL 區塊\n\n", *mode, len(results))
	fmt.Printf("%-8s %-8s %s\n", "驅動曲號", "呼叫端", "ECL 區塊")
	for song := byte(0); song < 16; song++ {
		areas, ok := byTrack[song]
		if !ok {
			continue
		}
		fmt.Printf("%8d %8d  %v\n", song, song+1, areas)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
