package web

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
)

//go:embed static/*
var staticFiles embed.FS

//go:embed templates/*
var templateFiles embed.FS

// GetFS returns the fs.FS pointing to the static assets
func GetFS() (fs.FS, error) {
	return fs.Sub(staticFiles, "static")
}

func toFloat64(v interface{}) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case uint64:
		return float64(n)
	case uint32:
		return float64(n)
	case uint:
		return float64(n)
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil {
			return f
		}
		return 0
	default:
		return 0
	}
}

func toUint64(v interface{}) uint64 {
	switch n := v.(type) {
	case uint64:
		return n
	case uint32:
		return uint64(n)
	case uint:
		return uint64(n)
	case int64:
		if n > 0 {
			return uint64(n)
		}
		return 0
	case int:
		if n > 0 {
			return uint64(n)
		}
		return 0
	case float64:
		if n > 0 {
			return uint64(n)
		}
		return 0
	default:
		return 0
	}
}

func toInt64(v interface{}) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case uint64:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return 0
	}
}

func getMapOrField(item interface{}, key string) string {
	if item == nil {
		return ""
	}
	v := reflect.ValueOf(item)
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return ""
		}
		v = v.Elem()
	}
	if v.Kind() == reflect.Map {
		for _, k := range v.MapKeys() {
			if strings.EqualFold(fmt.Sprint(k.Interface()), key) {
				val := v.MapIndex(k)
				if val.IsValid() {
					return fmt.Sprint(val.Interface())
				}
			}
		}
	} else if v.Kind() == reflect.Struct {
		f := v.FieldByNameFunc(func(n string) bool {
			return strings.EqualFold(n, key)
		})
		if f.IsValid() {
			return fmt.Sprint(f.Interface())
		}
	}
	return ""
}

func iterateSlice(slice interface{}, fn func(item interface{}) bool) {
	if slice == nil {
		return
	}
	v := reflect.ValueOf(slice)
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return
		}
		v = v.Elem()
	}
	if v.Kind() == reflect.Slice || v.Kind() == reflect.Array {
		for i := 0; i < v.Len(); i++ {
			if !fn(v.Index(i).Interface()) {
				break
			}
		}
	}
}

func funcMap() template.FuncMap {
	return template.FuncMap{
		"formatBytes": func(v interface{}) string {
			b := toUint64(v)
			const unit = 1024
			if b < unit {
				return fmt.Sprintf("%d B", b)
			}
			div, exp := uint64(unit), 0
			for n := b / unit; n >= unit; n /= unit {
				div *= unit
				exp++
			}
			val := float64(b) / float64(div)
			formatted := fmt.Sprintf("%.2f", val)
			formatted = strings.TrimRight(strings.TrimRight(formatted, "0"), ".")
			return fmt.Sprintf("%s %ciB", formatted, "KMGTPE"[exp])
		},
		"baseName": func(s string) string {
			parts := strings.Split(s, "/")
			return parts[len(parts)-1]
		},
		"datasetDepth": func(s string) int {
			return strings.Count(s, "/")
		},
		"hasChildDatasets": func(dsName string, all interface{}) bool {
			prefix := dsName + "/"
			found := false
			iterateSlice(all, func(item interface{}) bool {
				name := getMapOrField(item, "Name")
				if strings.HasPrefix(name, prefix) {
					found = true
					return false
				}
				return true
			})
			return found
		},
		"datasetUsage": func(dsName string, mountpoint string, smbShares interface{}, nfsExports interface{}, allDatasets interface{}) string {
			// Check direct SMB share
			directSmb := false
			iterateSlice(smbShares, func(item interface{}) bool {
				path := getMapOrField(item, "Path")
				name := getMapOrField(item, "Name")
				if path == mountpoint || path == "/mnt/"+dsName || path == "/"+dsName || name == dsName || strings.HasSuffix(dsName, "/"+name) {
					directSmb = true
					return false
				}
				return true
			})
			if directSmb {
				return "SMB"
			}

			// Check direct NFS export
			directNfs := false
			iterateSlice(nfsExports, func(item interface{}) bool {
				path := getMapOrField(item, "Path")
				if path == mountpoint || path == "/mnt/"+dsName || path == "/"+dsName {
					directNfs = true
					return false
				}
				return true
			})
			if directNfs {
				return "NFS"
			}

			// Check if any descendant is shared
			prefix := dsName + "/"
			hasChildShare := false
			iterateSlice(allDatasets, func(dItem interface{}) bool {
				dName := getMapOrField(dItem, "Name")
				dMount := getMapOrField(dItem, "Mountpoint")
				if strings.HasPrefix(dName, prefix) {
					iterateSlice(smbShares, func(sItem interface{}) bool {
						sPath := getMapOrField(sItem, "Path")
						sName := getMapOrField(sItem, "Name")
						if sPath == dMount || sPath == "/mnt/"+dName || sPath == "/"+dName || sName == dName || strings.HasSuffix(dName, "/"+sName) {
							hasChildShare = true
							return false
						}
						return true
					})
					if hasChildShare {
						return false
					}
					iterateSlice(nfsExports, func(nItem interface{}) bool {
						nPath := getMapOrField(nItem, "Path")
						if nPath == dMount || nPath == "/mnt/"+dName || nPath == "/"+dName {
							hasChildShare = true
							return false
						}
						return true
					})
					if hasChildShare {
						return false
					}
				}
				return true
			})

			if hasChildShare {
				return "TREE"
			}
			return ""
		},
		"formatRate": func(v interface{}) string {
			bps := toFloat64(v)
			if bps < 1024 {
				return fmt.Sprintf("%.2f B/s", bps)
			} else if bps < 1024*1024 {
				return fmt.Sprintf("%.2f kb/s", bps/1024*8)
			}
			return fmt.Sprintf("%.2f Mb/s", bps/(1024*1024)*8)
		},
		"formatUptime": func(v interface{}) string {
			sec := toInt64(v)
			if sec <= 0 {
				return "0m"
			}
			days := sec / 86400
			hours := (sec % 86400) / 3600
			mins := (sec % 3600) / 60
			if days > 0 {
				return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
			}
			if hours > 0 {
				return fmt.Sprintf("%dh %dm", hours, mins)
			}
			return fmt.Sprintf("%dm", mins)
		},
		"formatPercent": func(v interface{}) string {
			pct := toFloat64(v)
			return fmt.Sprintf("%.1f", pct)
		},
		"formatTime": func(v interface{}) string {
			switch t := v.(type) {
			case time.Time:
				if t.IsZero() {
					return "-"
				}
				return t.Local().Format("2006-01-02 15:04")
			case *time.Time:
				if t == nil || t.IsZero() {
					return "-"
				}
				return t.Local().Format("2006-01-02 15:04")
			case string:
				if strings.TrimSpace(t) == "" {
					return "-"
				}
				return t
			default:
				return "-"
			}
		},
		"nowTime": func() time.Time {
			return time.Now()
		},
		"formatDateTimeSec": func(t time.Time) string {
			if t.IsZero() {
				return "-"
			}
			return t.Local().Format("2006-01-02 15:04:05")
		},
		"timeSubSec": func(t time.Time, sec int) time.Time {
			return t.Add(-time.Duration(sec) * time.Second)
		},
		"calcTempY": func(tempC int, height float64) float64 {
			if tempC <= 0 {
				return height - 4.0
			}
			maxTemp := 50.0
			val := float64(tempC)
			if val > maxTemp {
				val = maxTemp
			}
			return height - ((val / maxTemp) * (height - 8.0)) - 4.0
		},
		"safeTempC": func(v interface{}) int {
			if v == nil {
				return 35
			}
			t := int(toFloat64(v))
			if t <= 0 {
				return 35
			}
			return t
		},
		"lower":     strings.ToLower,
		"upper":     strings.ToUpper,
		"join":      strings.Join,
		"hasPrefix": strings.HasPrefix,
		"contains":  strings.Contains,
		"hasString": func(slice interface{}, target string) bool {
			found := false
			iterateSlice(slice, func(item interface{}) bool {
				if fmt.Sprint(item) == target {
					found = true
					return false
				}
				return true
			})
			return found
		},
		"domId": func(s string) string {
			var b strings.Builder
			for _, r := range s {
				if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
					b.WriteRune(r)
				} else {
					b.WriteByte('_')
				}
			}
			if b.Len() == 0 {
				return "empty"
			}
			return b.String()
		},
		"seq": func(n int) []int {
			out := make([]int, n)
			for i := range out {
				out[i] = i
			}
			return out
		},
		"add": func(a, b interface{}) float64 {
			return toFloat64(a) + toFloat64(b)
		},
		"sub": func(a, b interface{}) float64 {
			return toFloat64(a) - toFloat64(b)
		},
		"mul": func(a, b interface{}) float64 {
			return toFloat64(a) * toFloat64(b)
		},
		"div": func(a, b interface{}) float64 {
			denom := toFloat64(b)
			if denom == 0 {
				return 0
			}
			return toFloat64(a) / denom
		},
		"gt": func(a, b interface{}) bool {
			return toFloat64(a) > toFloat64(b)
		},
		"lt": func(a, b interface{}) bool {
			return toFloat64(a) < toFloat64(b)
		},
		"gte": func(a, b interface{}) bool {
			return toFloat64(a) >= toFloat64(b)
		},
		"lte": func(a, b interface{}) bool {
			return toFloat64(a) <= toFloat64(b)
		},
		"clampPercent": func(v interface{}) string {
			f := toFloat64(v)
			if f < 1.0 {
				return "1"
			}
			if f > 100.0 {
				return "100"
			}
			return fmt.Sprintf("%.1f", f)
		},
		"dict": func(values ...interface{}) (map[string]interface{}, error) {
			if len(values)%2 != 0 {
				return nil, fmt.Errorf("invalid dict call")
			}
			dict := make(map[string]interface{}, len(values)/2)
			for i := 0; i < len(values); i += 2 {
				key, ok := values[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict keys must be strings")
				}
				dict[key] = values[i+1]
			}
			return dict, nil
		},
		"getDeviceIO": func(devices interface{}, name string) interface{} {
			if devices == nil {
				return nil
			}
			v := reflect.ValueOf(devices)
			if v.Kind() == reflect.Map {
				elem := v.MapIndex(reflect.ValueOf(name))
				if elem.IsValid() {
					return elem.Interface()
				}
			}
			return nil
		},
		"deviceReadMB": func(io interface{}) float64 {
			if io == nil {
				return 0.0
			}
			v := reflect.ValueOf(io)
			if v.Kind() == reflect.Ptr {
				if v.IsNil() {
					return 0.0
				}
				v = v.Elem()
			}
			if v.Kind() == reflect.Struct {
				f := v.FieldByName("ReadMBS")
				if f.IsValid() {
					return toFloat64(f.Interface())
				}
			} else if v.Kind() == reflect.Map {
				m := v.MapIndex(reflect.ValueOf("ReadMBS"))
				if m.IsValid() {
					return toFloat64(m.Interface())
				}
			}
			return 0.0
		},
		"deviceWriteMB": func(io interface{}) float64 {
			if io == nil {
				return 0.0
			}
			v := reflect.ValueOf(io)
			if v.Kind() == reflect.Ptr {
				if v.IsNil() {
					return 0.0
				}
				v = v.Elem()
			}
			if v.Kind() == reflect.Struct {
				f := v.FieldByName("WriteMBS")
				if f.IsValid() {
					return toFloat64(f.Interface())
				}
			} else if v.Kind() == reflect.Map {
				m := v.MapIndex(reflect.ValueOf("WriteMBS"))
				if m.IsValid() {
					return toFloat64(m.Interface())
				}
			}
			return 0.0
		},
		"deviceReadIOPS": func(io interface{}) int64 {
			if io == nil {
				return 0
			}
			v := reflect.ValueOf(io)
			if v.Kind() == reflect.Ptr {
				if v.IsNil() {
					return 0
				}
				v = v.Elem()
			}
			if v.Kind() == reflect.Struct {
				f := v.FieldByName("ReadIOPS")
				if f.IsValid() {
					return int64(toFloat64(f.Interface()))
				}
			} else if v.Kind() == reflect.Map {
				m := v.MapIndex(reflect.ValueOf("ReadIOPS"))
				if m.IsValid() {
					return int64(toFloat64(m.Interface()))
				}
			}
			return 0
		},
		"deviceWriteIOPS": func(io interface{}) int64 {
			if io == nil {
				return 0
			}
			v := reflect.ValueOf(io)
			if v.Kind() == reflect.Ptr {
				if v.IsNil() {
					return 0
				}
				v = v.Elem()
			}
			if v.Kind() == reflect.Struct {
				f := v.FieldByName("WriteIOPS")
				if f.IsValid() {
					return int64(toFloat64(f.Interface()))
				}
			} else if v.Kind() == reflect.Map {
				m := v.MapIndex(reflect.ValueOf("WriteIOPS"))
				if m.IsValid() {
					return int64(toFloat64(m.Interface()))
				}
			}
			return 0
		},
		"deviceHistory": func(io interface{}) []float64 {
			if io == nil {
				return nil
			}
			v := reflect.ValueOf(io)
			if v.Kind() == reflect.Ptr {
				if v.IsNil() {
					return nil
				}
				v = v.Elem()
			}
			if v.Kind() == reflect.Struct {
				f := v.FieldByName("History")
				if f.IsValid() {
					if s, ok := f.Interface().([]float64); ok {
						return s
					}
				}
			} else if v.Kind() == reflect.Map {
				m := v.MapIndex(reflect.ValueOf("History"))
				if m.IsValid() {
					if s, ok := m.Interface().([]float64); ok {
						return s
					}
				}
			}
			return nil
		},
		"deviceReadHistory": func(io interface{}) []float64 {
			if io == nil {
				return nil
			}
			v := reflect.ValueOf(io)
			if v.Kind() == reflect.Ptr {
				if v.IsNil() {
					return nil
				}
				v = v.Elem()
			}
			if v.Kind() == reflect.Struct {
				f := v.FieldByName("ReadHistory")
				if f.IsValid() {
					if s, ok := f.Interface().([]float64); ok {
						return s
					}
				}
			} else if v.Kind() == reflect.Map {
				m := v.MapIndex(reflect.ValueOf("ReadHistory"))
				if m.IsValid() {
					if s, ok := m.Interface().([]float64); ok {
						return s
					}
				}
			}
			return nil
		},
		"deviceWriteHistory": func(io interface{}) []float64 {
			if io == nil {
				return nil
			}
			v := reflect.ValueOf(io)
			if v.Kind() == reflect.Ptr {
				if v.IsNil() {
					return nil
				}
				v = v.Elem()
			}
			if v.Kind() == reflect.Struct {
				f := v.FieldByName("WriteHistory")
				if f.IsValid() {
					if s, ok := f.Interface().([]float64); ok {
						return s
					}
				}
			} else if v.Kind() == reflect.Map {
				m := v.MapIndex(reflect.ValueOf("WriteHistory"))
				if m.IsValid() {
					if s, ok := m.Interface().([]float64); ok {
						return s
					}
				}
			}
			return nil
		},
		"deviceMaxRead": func(io interface{}) float64 {
			if io == nil {
				return 0.0
			}
			v := reflect.ValueOf(io)
			if v.Kind() == reflect.Ptr {
				if v.IsNil() {
					return 0.0
				}
				v = v.Elem()
			}
			if v.Kind() == reflect.Struct {
				f := v.FieldByName("MaxReadMBS")
				if f.IsValid() {
					return toFloat64(f.Interface())
				}
			} else if v.Kind() == reflect.Map {
				m := v.MapIndex(reflect.ValueOf("MaxReadMBS"))
				if m.IsValid() {
					return toFloat64(m.Interface())
				}
			}
			return 0.0
		},
		"deviceMeanRead": func(io interface{}) float64 {
			if io == nil {
				return 0.0
			}
			v := reflect.ValueOf(io)
			if v.Kind() == reflect.Ptr {
				if v.IsNil() {
					return 0.0
				}
				v = v.Elem()
			}
			if v.Kind() == reflect.Struct {
				f := v.FieldByName("MeanReadMBS")
				if f.IsValid() {
					return toFloat64(f.Interface())
				}
			} else if v.Kind() == reflect.Map {
				m := v.MapIndex(reflect.ValueOf("MeanReadMBS"))
				if m.IsValid() {
					return toFloat64(m.Interface())
				}
			}
			return 0.0
		},
		"deviceMaxWrite": func(io interface{}) float64 {
			if io == nil {
				return 0.0
			}
			v := reflect.ValueOf(io)
			if v.Kind() == reflect.Ptr {
				if v.IsNil() {
					return 0.0
				}
				v = v.Elem()
			}
			if v.Kind() == reflect.Struct {
				f := v.FieldByName("MaxWriteMBS")
				if f.IsValid() {
					return toFloat64(f.Interface())
				}
			} else if v.Kind() == reflect.Map {
				m := v.MapIndex(reflect.ValueOf("MaxWriteMBS"))
				if m.IsValid() {
					return toFloat64(m.Interface())
				}
			}
			return 0.0
		},
		"deviceMeanWrite": func(io interface{}) float64 {
			if io == nil {
				return 0.0
			}
			v := reflect.ValueOf(io)
			if v.Kind() == reflect.Ptr {
				if v.IsNil() {
					return 0.0
				}
				v = v.Elem()
			}
			if v.Kind() == reflect.Struct {
				f := v.FieldByName("MeanWriteMBS")
				if f.IsValid() {
					return toFloat64(f.Interface())
				}
			} else if v.Kind() == reflect.Map {
				m := v.MapIndex(reflect.ValueOf("MeanWriteMBS"))
				if m.IsValid() {
					return toFloat64(m.Interface())
				}
			}
			return 0.0
		},
		"formatRateHuman": func(mbPerSec float64) string {
			if mbPerSec <= 0.00001 {
				return "0 B/s"
			}
			kib := mbPerSec * 1024.0
			if kib < 1000.0 {
				return fmt.Sprintf("%.0f KiB/s", kib)
			}
			if mbPerSec < 10.0 {
				return fmt.Sprintf("%.2f MiB/s", mbPerSec)
			}
			return fmt.Sprintf("%.1f MiB/s", mbPerSec)
		},
		"truenasChartTicks": func(rHist, wHist interface{}) []string {
			var maxVal float64 = 0.5
			extractMax := func(v interface{}) {
				if v == nil {
					return
				}
				if s, ok := v.([]float64); ok {
					for _, val := range s {
						if val > maxVal {
							maxVal = val
						}
					}
				}
			}
			extractMax(rHist)
			extractMax(wHist)
			maxKiB := maxVal * 1024.0
			var topTick float64
			if maxKiB <= 200 {
				topTick = 200
			} else if maxKiB <= 400 {
				topTick = 400
			} else if maxKiB <= 800 {
				topTick = 800
			} else if maxKiB <= 1500 {
				topTick = 1500
			} else {
				topTick = math.Ceil(maxKiB/1000.0) * 1000.0
			}
			return []string{
				fmt.Sprintf("%.0f", topTick),
				fmt.Sprintf("%.0f", topTick*0.75),
				fmt.Sprintf("%.0f", topTick*0.5),
				fmt.Sprintf("%.0f", topTick*0.25),
				"0",
			}
		},
		"truenasAreaPath": func(values interface{}, maxScaleKiB, width, height float64) string {
			var vals []float64
			if values != nil {
				if s, ok := values.([]float64); ok {
					vals = s
				}
			}
			if len(vals) < 2 {
				return fmt.Sprintf("M 0,%.1f L %.1f,%.1f L %.1f,%.1f L 0,%.1f Z", height, width, height, width, height, height)
			}
			maxMB := maxScaleKiB / 1024.0
			if maxMB <= 0 {
				maxMB = 1.0
			}
			step := width / float64(len(vals)-1)
			var sb strings.Builder
			for i, v := range vals {
				x := float64(i) * step
				clamped := v
				if clamped > maxMB {
					clamped = maxMB
				}
				y := height - ((clamped / maxMB) * (height - 6.0)) - 3.0
				if i == 0 {
					sb.WriteString(fmt.Sprintf("M %.1f,%.1f", x, y))
				} else {
					sb.WriteString(fmt.Sprintf(" L %.1f,%.1f", x, y))
				}
			}
			sb.WriteString(fmt.Sprintf(" L %.1f,%.1f L 0,%.1f Z", width, height, height))
			return sb.String()
		},
		"truenasLinePath": func(values interface{}, maxScaleKiB, width, height float64) string {
			var vals []float64
			if values != nil {
				if s, ok := values.([]float64); ok {
					vals = s
				}
			}
			if len(vals) < 2 {
				return fmt.Sprintf("M 0,%.1f L %.1f,%.1f", height, width, height)
			}
			maxMB := maxScaleKiB / 1024.0
			if maxMB <= 0 {
				maxMB = 1.0
			}
			step := width / float64(len(vals)-1)
			var sb strings.Builder
			for i, v := range vals {
				x := float64(i) * step
				clamped := v
				if clamped > maxMB {
					clamped = maxMB
				}
				y := height - ((clamped / maxMB) * (height - 6.0)) - 3.0
				if i == 0 {
					sb.WriteString(fmt.Sprintf("M %.1f,%.1f", x, y))
				} else {
					sb.WriteString(fmt.Sprintf(" L %.1f,%.1f", x, y))
				}
			}
			return sb.String()
		},
		"truenasMaxScaleKiB": func(rHist, wHist interface{}) float64 {
			var maxVal float64 = 0.5
			extractMax := func(v interface{}) {
				if v == nil {
					return
				}
				if s, ok := v.([]float64); ok {
					for _, val := range s {
						if val > maxVal {
							maxVal = val
						}
					}
				}
			}
			extractMax(rHist)
			extractMax(wHist)
			maxKiB := maxVal * 1024.0
			if maxKiB <= 200 {
				return 200
			} else if maxKiB <= 400 {
				return 400
			} else if maxKiB <= 800 {
				return 800
			} else if maxKiB <= 1500 {
				return 1500
			}
			return math.Ceil(maxKiB/1000.0) * 1000.0
		},
		"sparklineSVG": func(values interface{}, width, height float64) string {
			var vals []float64
			if values != nil {
				if s, ok := values.([]float64); ok {
					vals = s
				}
			}
			if len(vals) < 2 {
				return fmt.Sprintf("M 0,%.1f L %.1f,%.1f", height, width, height)
			}
			maxVal := 1.0
			for _, v := range vals {
				if v > maxVal {
					maxVal = v
				}
			}
			step := width / float64(len(vals)-1)
			var sb strings.Builder
			for i, v := range vals {
				x := float64(i) * step
				y := height - ((v / maxVal) * (height - 4.0)) - 2.0
				if i == 0 {
					sb.WriteString(fmt.Sprintf("M %.1f,%.1f", x, y))
				} else {
					sb.WriteString(fmt.Sprintf(" L %.1f,%.1f", x, y))
				}
			}
			return sb.String()
		},
		"sparklineFillSVG": func(values interface{}, width, height float64) string {
			var vals []float64
			if values != nil {
				if s, ok := values.([]float64); ok {
					vals = s
				}
			}
			if len(vals) < 2 {
				return fmt.Sprintf("M 0,%.1f L %.1f,%.1f L %.1f,%.1f L 0,%.1f Z", height, width, height, width, height, height)
			}
			maxVal := 1.0
			for _, v := range vals {
				if v > maxVal {
					maxVal = v
				}
			}
			step := width / float64(len(vals)-1)
			var sb strings.Builder
			for i, v := range vals {
				x := float64(i) * step
				y := height - ((v / maxVal) * (height - 4.0)) - 2.0
				if i == 0 {
					sb.WriteString(fmt.Sprintf("M %.1f,%.1f", x, y))
				} else {
					sb.WriteString(fmt.Sprintf(" L %.1f,%.1f", x, y))
				}
			}
			sb.WriteString(fmt.Sprintf(" L %.1f,%.1f L 0,%.1f Z", width, height, height))
			return sb.String()
		},
	}
}

// Engine compiles and renders embedded Go templates
type Engine struct {
	tmpl *template.Template
}

// NewEngine creates a new template engine from embedded templates
func NewEngine() (*Engine, error) {
	root := template.New("").Funcs(funcMap())
	err := fs.WalkDir(templateFiles, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".html") {
			return nil
		}
		data, err := templateFiles.ReadFile(path)
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(path, "templates/")
		name = strings.ReplaceAll(name, "\\", "/")
		t := root.New(name)
		_, err = t.Parse(string(data))
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("failed to load templates: %w", err)
	}
	return &Engine{tmpl: root}, nil
}

// Render executes a named template to the provided writer
func (e *Engine) Render(w io.Writer, name string, data interface{}) error {
	name = strings.ReplaceAll(name, "\\", "/")
	t := e.tmpl.Lookup(name)
	if t == nil {
		return fmt.Errorf("template not found: %s", name)
	}
	return t.Execute(w, data)
}
