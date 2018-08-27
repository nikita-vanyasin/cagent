package cagent

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/cpu"
	"github.com/shirou/gopsutil/load"
	log "github.com/sirupsen/logrus"
)

const measureInterval = time.Second * 5
const cpuGetUtilisationTimeout = time.Second * 10

var utilisationMetricsByOS = map[string][]string{
	"windows": {"system", "user", "idle", "irq"},
	"linux":   {"system", "user", "nice", "iowait", "idle", "softirq", "irq"},
	"freebsd": {"system", "user", "nice", "idle", "irq"},
	"solaris": {},
	"openbsd": {"system", "user", "nice", "idle", "irq"},
	"darwin":  {"system", "user", "nice", "idle"},
}

type ValuesMap map[string]float64
type ValuesCount map[string]int

type TimeValue struct {
	Time   time.Time
	Values ValuesMap
}

type TimeSeriesAverage struct {
	TimeSeries         []TimeValue
	mu                 sync.Mutex
	_DurationInMinutes []int // do not set directly, use SetDurationsMinutes
}

type CPUWatcher struct {
	LoadAvg1  bool
	LoadAvg5  bool
	LoadAvg15 bool

	UtilAvg   TimeSeriesAverage
	UtilTypes []string
}

var utilisationMetricsByOSMap = make(map[string]map[string]struct{})

func (tsa *TimeSeriesAverage) SetDurationsMinutes(durations ...int) {
	tsa._DurationInMinutes = durations
	sort.Ints(durations)
}

func init() {
	for osName, metrics := range utilisationMetricsByOS {
		utilisationMetricsByOSMap[osName] = make(map[string]struct{})
		for _, metric := range metrics {
			utilisationMetricsByOSMap[osName][metric] = struct{}{}
		}
	}
}

func minutes(mins int) time.Duration {
	return time.Duration(time.Minute * time.Duration(mins))
}

func (tsa *TimeSeriesAverage) Add(t time.Time, valuesMap ValuesMap) {
	for {
		if len(tsa.TimeSeries) > 0 && time.Since(tsa.TimeSeries[0].Time) > minutes(tsa._DurationInMinutes[len(tsa._DurationInMinutes)-1]) {
			tsa.TimeSeries = tsa.TimeSeries[1:]
		} else {
			break
		}
	}
	tsa.TimeSeries = append(tsa.TimeSeries, TimeValue{t, valuesMap})
}

func (tsa *TimeSeriesAverage) Average() map[int]ValuesMap {
	sum := make(map[int]ValuesMap)
	count := make(map[int]ValuesCount)

	for _, d := range tsa._DurationInMinutes {
		sum[d] = make(ValuesMap)
		count[d] = make(ValuesCount)
	}
	for _, ts := range tsa.TimeSeries {
		n := time.Now()

		for _, d := range tsa._DurationInMinutes {
			if n.Sub(ts.Time) < minutes(d) {
				for key, val := range ts.Values {
					sum[d][key] += val
					count[d][key]++
				}
			}
		}
	}

	for _, d := range tsa._DurationInMinutes {
		for key, val := range sum[d] {
			sum[d][key] = val / float64(count[d][key])
		}
	}

	return sum
}

func (tsa *TimeSeriesAverage) Percentage() (map[int]ValuesMap, error) {
	sum := make(map[int]ValuesMap)

	tsa.mu.Lock()
	defer tsa.mu.Unlock()
	if len(tsa.TimeSeries) == 0 {
		return nil, errors.New("CPU metrics are not collected yet")
	}
	last := tsa.TimeSeries[len(tsa.TimeSeries)-1]
	for _, d := range tsa._DurationInMinutes {
		sum[d] = make(ValuesMap)
		keyInt := len(tsa.TimeSeries) - int(int64(d)*int64(time.Minute)/int64(measureInterval))

		if keyInt < 0 {
			log.Debugf("cpu.util metrics for %d min avg calculation are not collected yet", d)
		}

		for key, lastVal := range last.Values {
			if keyInt < 0 {
				sum[d][key] = -1
				continue
			}
			sum[d][key] = float64(int64(((lastVal-tsa.TimeSeries[keyInt].Values[key])/last.Time.Sub(tsa.TimeSeries[keyInt].Time).Seconds())*10000+0.5)) / 10000
		}
	}

	return sum, nil
}

func (ca *Cagent) CPUWatcher() CPUWatcher {
	stat := CPUWatcher{}
	stat.UtilAvg.mu.Lock()

	if len(ca.CPULoadDataGather) > 0 {
		_, err := load.Avg()

		if err != nil && err.Error() == "not implemented yet" {
			log.Errorf("[CPU] load_avg metric unavailable on %s", runtime.GOOS)
		} else {
			for _, d := range ca.CPULoadDataGather {
				if strings.HasPrefix(d, "avg") {
					v, _ := strconv.Atoi(d[3:])

					switch v {
					case 1:
						stat.LoadAvg1 = true
					case 5:
						stat.LoadAvg5 = true
					case 15:
						stat.LoadAvg15 = true
					default:
						log.Errorf("[CPU] wrong cpu_load_data_gathering_mode. Supported values: avg1, avg5, avg15")
					}
				}
			}
		}
	}

	durations := []int{}
	for _, d := range ca.CPUUtilDataGather {
		if strings.HasPrefix(d, "avg") {
			v, err := strconv.Atoi(d[3:])
			if err != nil {
				log.Errorf("[CPU] failed to parse cpu_load_data_gathering_mode '%s': %s", d, err.Error())
				continue
			}
			durations = append(durations, v)
		}
	}

	for _, t := range ca.CPUUtilTypes {
		found := false

		for _, metric := range utilisationMetricsByOS[runtime.GOOS] {
			if metric == t {
				found = true
				break
			}
		}

		if !found {
			log.Errorf("[CPU] utilisation metric '%s' not implemented on %s", t, runtime.GOOS)
		} else {
			stat.UtilTypes = append(stat.UtilTypes, t)
		}
	}

	stat.UtilAvg.SetDurationsMinutes(durations...)
	stat.UtilAvg.mu.Unlock()

	return stat
}

func (stat *CPUWatcher) Once() error {

	stat.UtilAvg.mu.Lock()

	ctx, _ := context.WithTimeout(context.Background(), cpuGetUtilisationTimeout)
	times, err := cpu.TimesWithContext(ctx, true)

	if err != nil {
		return err
	}

	values := ValuesMap{}

	for i, cputime := range times {
		for _, utype := range stat.UtilTypes {
			utype = strings.ToLower(utype)
			var value float64
			switch utype {
			case "system":
				value = cputime.System
			case "user":
				value = cputime.User
			case "nice":
				value = cputime.Nice
			case "idle":
				value = cputime.Idle
			case "iowait":
				value = cputime.Iowait
			case "irq":
				value = cputime.Irq
			case "softirq":
				value = cputime.Softirq
			case "steal":
				value = cputime.Steal
			default:
				continue
			}
			values[fmt.Sprintf("%s.%%d.cpu%d", utype, i)] = value
			values[fmt.Sprintf("%s.%%d.total", utype)] += value
		}
	}

	for _, k := range []string{"system.%d.total", "user.%d.total", "nice.%d.total", "idle.%d.total", "iowait.%d.total", "interrupt.%d.total", "softirq.%d.total", "steal.%d.total"} {
		values[k] = values[k] / float64(len(times))
	}

	stat.UtilAvg.Add(time.Now(), values)
	stat.UtilAvg.mu.Unlock()
	return nil
}

func (stat *CPUWatcher) Run() {
	for {
		err := stat.Once()
		if err != nil {
			log.Errorf("[CPU] Failed to read utilisation metrics: " + err.Error())
		}
		time.Sleep(measureInterval)
	}
}

func (cs *CPUWatcher) Results() (MeasurementsMap, error) {
	var errs []string
	util, err := cs.UtilAvg.Percentage()
	if err != nil {
		log.Errorf("[CPU] Failed to read utilisation metrics: " + err.Error())
		errs = append(errs, err.Error())
	}
	results := MeasurementsMap{}
	for d, m := range util {
		for k, v := range m {
			if v == -1 {
				results["util."+fmt.Sprintf(k, d)] = nil
			} else {
				results["util."+fmt.Sprintf(k, d)] = v
			}
		}
	}
	var loadAvg *load.AvgStat
	if cs.LoadAvg1 || cs.LoadAvg5 || cs.LoadAvg15 {
		loadAvg, err = load.Avg()
		if err != nil {
			log.Error("[CPU] Failed to read load_avg: ", err.Error())
			errs = append(errs, err.Error())
		} else {
			if cs.LoadAvg1 {
				results["load.avg.1"] = loadAvg.Load1
			}

			if cs.LoadAvg5 {
				results["load.avg.5"] = loadAvg.Load5
			}

			if cs.LoadAvg15 {
				results["load.avg.15"] = loadAvg.Load15
			}
		}
	}

	if len(errs) == 0 {
		return results, nil
	}

	return results, errors.New("CPU: " + strings.Join(errs, "; "))

}