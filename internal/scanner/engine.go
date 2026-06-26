package scanner

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

type ProgressFn func(path string, size int64)

func ScanDirectory(root string, maxDepth int, excludes []string, progress ProgressFn) (int64, error) {
	rootInfo, err := os.Stat(root)
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", root, err)
	}
	if !rootInfo.IsDir() {
		return rootInfo.Size(), nil
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, fmt.Errorf("read dir %s: %w", root, err)
	}

	excludeSet := buildExcludeSet(excludes)
	var wg sync.WaitGroup
	results := make(chan int64, len(entries))
	sem := make(chan struct{}, min(runtime.NumCPU()*2, 8))

	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if excludeSet[path] || excludeSet[entry.Name()] {
			if progress != nil {
				progress(path, 0)
			}
			continue
		}

		if entry.IsDir() {
			wg.Add(1)
			go func(p string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				size := walkDir(p, 1, maxDepth, excludeSet)
				results <- size
				if progress != nil {
					progress(p, size)
				}
			}(path)
		} else if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err == nil {
				results <- info.Size()
			}
		}
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var total int64
	for size := range results {
		total += size
	}
	return total, nil
}

func walkDir(path string, depth, maxDepth int, excludes map[string]bool) int64 {
	if maxDepth > 0 && depth > maxDepth {
		var size int64
		filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.Type().IsRegular() {
				info, err := d.Info()
				if err == nil {
					size += info.Size()
				}
			}
			return nil
		})
		return size
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return 0
	}

	var total int64
	for _, entry := range entries {
		p := filepath.Join(path, entry.Name())
		if excludes[p] || excludes[entry.Name()] {
			continue
		}

		if entry.IsDir() {
			total += walkDir(p, depth+1, maxDepth, excludes)
		} else if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err == nil {
				total += info.Size()
			}
		}
	}
	return total
}

func buildExcludeSet(excludes []string) map[string]bool {
	m := make(map[string]bool, len(excludes))
	for _, e := range excludes {
		m[e] = true
	}
	return m
}

func FormatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func FormatBytesShort(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ci", float64(b)/float64(div), "KMGTPE"[exp])
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
