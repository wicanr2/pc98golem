#!/usr/bin/env python3
"""把 pc98golem 實跑抽出來的演奏資料，對 golden-box-remake-engine 靜態解析的。

兩邊都要先產生輸出：

    # 執行器（本 repo）
    MSCDRV_DIR=<驅動目錄> tools/go.sh run ./cmd/pc98-music \
        -driver /driver/MSCDRV.EXE -out /src/.out -dump-blocks -max-blocks 64

    # 靜態解析（engine repo）
    MSCDRV_DIR=<驅動目錄> tools/go.sh run ./cmd/pc98-render-music \
        -driver /driver/MSCDRV.EXE -dump-blocks -block-limit 64 | tail -1 \
        > <本 repo>/.out/engine-blocks.json

判準是**逐筆相同**：每一首每一個聲道，區塊偏移的序列與長度都要一樣。
兩邊來源完全獨立——一邊跑原版的 8086 碼，一邊照格式重寫——所以對得上
才代表格式讀對了。
"""
import json, os, sys

def main(outdir):
    engine = json.load(open(os.path.join(outdir, 'engine-blocks.json')))
    same = diff = 0
    problems = []
    for track in sorted(int(k) for k in engine):
        path = os.path.join(outdir, 'blocks-%02d.json' % track)
        if not os.path.exists(path):
            problems.append('第 %d 首沒有執行器輸出' % track)
            continue
        run = json.load(open(path))
        static = engine[str(track)]
        for channel in range(len(static)):
            a = [b['offset'] for b in (run['channels'][channel] or [])]
            b = static[channel] or []
            if a == b:
                same += 1
                continue
            diff += 1
            first = next((i for i in range(min(len(a), len(b))) if a[i] != b[i]), min(len(a), len(b)))
            problems.append('第 %d 首聲道 %d：第 %d 筆起不同（執行 %d 筆、靜態 %d 筆）'
                            % (track, channel, first, len(a), len(b)))
    print('逐筆相同的聲道：%d／%d' % (same, same + diff))
    for problem in problems[:40]:
        print('  ', problem)
    return 0 if diff == 0 else 1

if __name__ == '__main__':
    sys.exit(main(sys.argv[1] if len(sys.argv) > 1 else '.out'))
