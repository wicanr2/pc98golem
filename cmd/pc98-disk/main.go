// pc98-disk 列出一份 PC-98 磁碟映像裡的檔案，並且**把讀不到的磁區印出來**。
//
//	tools/go.sh run ./cmd/pc98-disk <映像.fdd>
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/wicanr2/pc98golem/internal/fat"
	"github.com/wicanr2/pc98golem/internal/vfd"
)

func main() {
	asJSON := flag.Bool("json", false, "輸出 JSON")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "用法：pc98-disk [-json] <映像.fdd>")
		os.Exit(2)
	}
	image, err := vfd.Open(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	volume, err := fat.Mount(image)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	entries := volume.List()
	if *asJSON {
		report := map[string]any{
			"sector_size":    image.SectorSize(),
			"sectors":        image.Count(),
			"absent_sectors": image.Absent(),
			"files":          entries,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(report)
		return
	}
	fmt.Printf("每磁區 %d 位元組，讀得到 %d 個磁區\n", image.SectorSize(), image.Count())
	if absent := image.Absent(); len(absent) > 0 {
		fmt.Printf("**讀不到的磁區**：%v\n", absent)
	}
	fmt.Printf("\n%-14s %9s  %s\n", "檔名", "大小", "狀態")
	for _, entry := range entries {
		status := "ok"
		if entry.Damaged {
			status = fmt.Sprintf("**壞** 叢集 %v", entry.DamagedClusters)
		}
		fmt.Printf("%-14s %9d  %s\n", entry.Name, entry.Size, status)
	}
}
