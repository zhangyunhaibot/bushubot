package bot

import (
	"bytes"
	"fmt"
	"time"

	"bushubot-master/internal/model"

	"github.com/wcharczuk/go-chart/v2"
	"github.com/wcharczuk/go-chart/v2/drawing"
)

// renderMetricsChart 把 24h 内的快照画成 3 条线（内存%/磁盘%/负载 ratio）
func renderMetricsChart(customerName string, snaps []model.MetricsSnapshot, hours int) ([]byte, error) {
	if len(snaps) < 2 {
		return nil, fmt.Errorf("数据点不足（%d 条），等几次心跳再看", len(snaps))
	}

	xs := make([]time.Time, 0, len(snaps))
	mem := make([]float64, 0, len(snaps))
	disk := make([]float64, 0, len(snaps))
	load := make([]float64, 0, len(snaps))

	for _, s := range snaps {
		xs = append(xs, s.SnapshotAt)
		if s.MemTotalMB > 0 {
			mem = append(mem, float64(s.MemUsedMB)*100/float64(s.MemTotalMB))
		} else {
			mem = append(mem, 0)
		}
		if s.DiskTotalGB > 0 {
			disk = append(disk, float64(s.DiskUsedGB)*100/float64(s.DiskTotalGB))
		} else {
			disk = append(disk, 0)
		}
		// 负载用 load_1m / cpu_count * 100，超 100% 视为过载
		if s.CPUCount > 0 {
			load = append(load, s.Load1m/float64(s.CPUCount)*100)
		} else {
			load = append(load, 0)
		}
	}

	graph := chart.Chart{
		Title:  fmt.Sprintf("%s - 资源历史 (最近 %dh)", customerName, hours),
		Width:  900,
		Height: 480,
		Background: chart.Style{
			Padding: chart.Box{Top: 40, Left: 20, Right: 20, Bottom: 20},
		},
		XAxis: chart.XAxis{
			Style:          chart.Style{StrokeColor: drawing.ColorBlack, FontSize: 9},
			ValueFormatter: chart.TimeMinuteValueFormatter,
		},
		YAxis: chart.YAxis{
			Style:     chart.Style{StrokeColor: drawing.ColorBlack, FontSize: 9},
			Range:     &chart.ContinuousRange{Min: 0, Max: 100},
			NameStyle: chart.Style{FontSize: 9},
			Name:      "%",
		},
		Series: []chart.Series{
			chart.TimeSeries{
				Name:    "内存%",
				Style:   chart.Style{StrokeColor: drawing.ColorBlue, StrokeWidth: 1.5},
				XValues: xs,
				YValues: mem,
			},
			chart.TimeSeries{
				Name:    "磁盘%",
				Style:   chart.Style{StrokeColor: drawing.ColorRed, StrokeWidth: 1.5},
				XValues: xs,
				YValues: disk,
			},
			chart.TimeSeries{
				Name:    "负载/CPU%",
				Style:   chart.Style{StrokeColor: drawing.ColorGreen, StrokeWidth: 1.5},
				XValues: xs,
				YValues: load,
			},
		},
	}
	graph.Elements = []chart.Renderable{
		chart.Legend(&graph),
	}

	buf := bytes.NewBuffer(nil)
	if err := graph.Render(chart.PNG, buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
