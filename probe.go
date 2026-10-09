package main

// Диагностика запуска: doq-web --probe
//
// На роутерах Go-процесс может умирать молча (SIGILL без эмулятора FPU,
// SIGKILL от OOM-killer), поэтому проверка делает три вещи и печатает
// результат каждой: читает /proc/cpuinfo, читает /proc/meminfo, аллоцирует
// память. Если до аллокации дошло, а сервис не поднимается — причина не в
// рантайме Go, а в занятом порту или в параметрах запуска.

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strings"
)

func readProc(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "нечитаем: " + err.Error()
	}
	return string(data)
}

// cpuSummary печатает строки cpuinfo, по которым видно наличие FPU.
func cpuSummary() {
	raw := readProc("/proc/cpuinfo")
	if strings.HasPrefix(raw, "нечитаем") {
		fmt.Println("cpu:", raw)
		return
	}
	for _, line := range strings.Split(raw, "\n") {
		for _, key := range []string{"isa", "cpu model", "system type", "machine", "FPU", "ASEs implemented"} {
			if strings.HasPrefix(strings.ToLower(line), strings.ToLower(key)+":") {
				fmt.Println("cpu:", strings.TrimSpace(line))
			}
		}
	}
}

// memAvailable возвращает MemAvailable в байтах
// (fallback для старых ядер: MemFree+Buffers+Cached).
func memAvailable() (int64, error) {
	raw := readProc("/proc/meminfo")
	if strings.HasPrefix(raw, "нечитаем") {
		return 0, fmt.Errorf("%s", raw)
	}
	var avail, fallback int64
	sc := bufio.NewScanner(strings.NewReader(raw))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		var kb int64
		if _, err := fmt.Sscanf(fields[1], "%d", &kb); err != nil {
			continue
		}
		switch fields[0] {
		case "MemAvailable:":
			avail = kb * 1024
		case "MemFree:", "Buffers:", "Cached:":
			fallback += kb * 1024
		}
	}
	switch {
	case avail > 0:
		return avail, nil
	case fallback > 0:
		return fallback, nil
	}
	return 0, fmt.Errorf("MemAvailable не найден")
}

func runProbe() int {
	fmt.Printf("go runtime ok: %s %s %s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	cpuSummary()

	avail, err := memAvailable()
	if err != nil {
		fmt.Println("meminfo:", err)
	} else {
		fmt.Printf("meminfo: MemAvailable=%d MB\n", avail/(1<<20))
	}

	// Проверяем доступность кучи: 4 МБ суммой чанков, keep/sum нужны,
	// чтобы компилятор не выкинул цикл как мёртвый код.
	const chunk = 64 << 10
	var sum int
	keep := make([][]byte, 0, 64)
	for i := 0; i < 4*(1<<20)/chunk; i++ {
		b := make([]byte, chunk)
		b[0] = byte(i)
		sum += int(b[0])
		keep = append(keep, b)
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	fmt.Printf("alloc 4MB ok (sum=%d), heapAlloc=%d MB, sys=%d MB\n",
		sum, ms.HeapAlloc/(1<<20), ms.Sys/(1<<20))
	runtime.KeepAlive(keep)

	fmt.Println("PROBE OK")
	return 0
}
