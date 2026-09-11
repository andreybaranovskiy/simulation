package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/andreybaranovskiy/simulation/internal/runstore"
)

// buildProgress reports the post-processing pass on one line. The build reads
// the whole trace, which on a long run takes longer than the simulation did,
// so silence here would look like a hang.
func buildProgress(quiet bool) func(float64, string) {
	if quiet {
		return nil
	}
	return func(fraction float64, stage string) {
		fmt.Fprintf(os.Stderr, "\r  %5.1f%%  %-30s", fraction*100, stage)
	}
}

// printArtifacts summarises what a run left on disk. The per-level peak matters
// most: it is what decides whether a browser can render that level at all.
func printArtifacts(w *os.File, m *runstore.Manifest) {
	fmt.Fprintf(w, "\r%-60s\r", "")
	fmt.Fprintf(w, "  playback: %d chunks across %d levels, %s on disk\n",
		m.Counts.Chunks, len(m.Levels), formatBytes(m.Counts.TotalBytes))

	for _, l := range m.Levels {
		fmt.Fprintf(w, "    level %d: 1 entity in %-3d  %4d chunks  %9s  peak %d on screen\n",
			l.Index, l.Stride, l.ChunkCount, formatBytes(l.Bytes), l.PeakLive)
	}

	if len(m.Heatmaps) > 0 {
		names := make([]string, 0, len(m.Heatmaps))
		for _, h := range m.Heatmaps {
			names = append(names, h.Label)
		}
		fmt.Fprintf(w, "  heatmaps: %s at %g m cells\n",
			strings.Join(names, ", "), m.Heatmaps[0].CellMeters)
	}

	for _, warning := range m.Warnings {
		fmt.Fprintf(w, "  note: %s\n", warning)
	}
}

func formatBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.0f KB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
	return fmt.Sprintf("%.2f GB", float64(n)/(1024*1024*1024))
}
