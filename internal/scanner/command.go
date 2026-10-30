package scanner

import (
	"fmt"
	"strings"
)

// ExecCommand never interpolates caller values into shell source.
// The path argument selects scratch space; its basename is never overwritten.
func ExecCommand(scannerPath, mountPath string, maxDepth int, excludes []string, workers int) ([]string, error) {
	return scannerExecCommand(scannerPath, mountPath, maxDepth, excludes, workers, "")
}

// CompressedExecCommand is ExecCommand for a gzipped scanner on
// stdin. It exits 124 without reading stdin if the pod has no gunzip.
func CompressedExecCommand(scannerPath, mountPath string, maxDepth int, excludes []string, workers int) ([]string, error) {
	return scannerExecCommand(scannerPath, mountPath, maxDepth, excludes, workers, "gzip")
}

func scannerExecCommand(scannerPath, mountPath string, maxDepth int, excludes []string, workers int, encoding string) ([]string, error) {
	if maxDepth < 0 || workers < 0 {
		return nil, fmt.Errorf("max-depth and workers must be nonnegative")
	}
	for _, exclude := range excludes {
		if strings.ContainsAny(exclude, ",\n") {
			return nil, fmt.Errorf("exclude pattern %q contains a comma or newline", exclude)
		}
	}
	excludeStr := strings.Join(append(append([]string{}, excludes...), ".pvdu-scan.*"), ",")
	script := `umask 077
if [ "$6" = gzip ]; then command -v gunzip >/dev/null 2>&1 || exit 124; fi
dir=$(mktemp -d "${1%/*}/.pvdu-scan.XXXXXX") || exit 125
trap 'rm -rf "$dir"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
if [ "$6" = gzip ]; then gunzip -c; else cat; fi > "$dir/scanner" && chmod 700 "$dir/scanner" || exit 125
printf '%s\n' '{"type":"scan-start"}'
"$dir/scanner" "$2" --max-depth="$3" --exclude="$4" --workers="$5" --output=json-lines`
	return []string{"sh", "-c", script, "pvdu-sh", scannerPath, mountPath, fmt.Sprint(maxDepth), excludeStr, fmt.Sprint(workers), encoding}, nil
}
