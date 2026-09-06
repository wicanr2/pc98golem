// pc98-disk 列出一份 PC-98 磁碟映像裡的檔案，並且**把讀不到的磁區印出來**。
//
// 容器自動判別：VFD（`.fdd`）與 D88 都吃。
//
//	tools/go.sh run ./cmd/pc98-disk <映像>
//	tools/go.sh run ./cmd/pc98-disk -extract MUSIC.EXE -out /tmp <映像>
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/wicanr2/pc98golem/internal/d88"
	"github.com/wicanr2/pc98golem/internal/disk"
	"github.com/wicanr2/pc98golem/internal/fat"
	"github.com/wicanr2/pc98golem/internal/vfd"
)

func main() {
	asJSON := flag.Bool("json", false, "輸出 JSON")
	extract := flag.String("extract", "", "把這個檔案取出來（`all` = 全部）")
	outDir := flag.String("out", ".", "取出來的檔案放哪")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "用法：pc98-disk [-json] [-extract 檔名|all] [-out 目錄] <映像>")
		os.Exit(2)
	}
	image, container, err := open(flag.Arg(0))
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

	if *extract != "" {
		if err := extractFiles(volume, entries, *extract, *outDir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	if *asJSON {
		report := map[string]any{
			"container":      container,
			"geometry":       string(volume.Geometry),
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
	fmt.Printf("容器 %s，每磁區 %d 位元組，讀得到 %d 個磁區，版面來源 %s\n",
		container, image.SectorSize(), image.Count(), volume.Geometry)
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

// open 依內容判別容器。**不看副檔名**——同一種容器換個副檔名還是同一種東西。
func open(path string) (disk.Image, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	if len(raw) >= 7 && string(raw[:7]) == "VFD1.00" {
		image, err := vfd.Parse(raw)
		return image, "VFD", err
	}
	if d88.Looks(raw) {
		image, err := d88.Parse(raw)
		return image, "D88", err
	}
	return nil, "", fmt.Errorf("%s：認不出容器（不是 VFD，版面也不像 D88）", filepath.Base(path))
}

func extractFiles(volume *fat.Volume, entries []fat.Entry, which, outDir string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	for _, entry := range entries {
		if which != "all" && !equalFold(entry.Name, which) {
			continue
		}
		data, err := volume.Read(entry.Name)
		if err != nil {
			if which != "all" {
				return err
			}
			fmt.Printf("%-14s 取不出來：%v\n", entry.Name, err)
			continue
		}
		target := filepath.Join(outDir, entry.Name)
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return err
		}
		fmt.Printf("%-14s %9d → %s\n", entry.Name, len(data), target)
	}
	return nil
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'a' <= x && x <= 'z' {
			x -= 32
		}
		if 'a' <= y && y <= 'z' {
			y -= 32
		}
		if x != y {
			return false
		}
	}
	return true
}
